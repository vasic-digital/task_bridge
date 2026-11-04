package syncengine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/vasic-digital/task_bridge/pkg/client"
	"github.com/vasic-digital/task_bridge/pkg/mapper"
	"github.com/vasic-digital/task_bridge/pkg/states"
)

func loadSpec(t testing.TB) *states.Spec {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "states", "testdata", "spec_repro.json"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := states.LoadSpec(b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// memTask is one task of the in-memory remote.
type memTask struct {
	id, name, desc, status string
	tags                   []string
	values                 map[string]string // custom-field id -> option name (drop_down) or text
}

// memClient is a stateful fake remote implementing client.Client AND
// client.StatesClient (fakes are permitted only in unit tests, §11.4.27). It
// records every call, so tests assert what was NOT called as precisely as what was.
type memClient struct {
	tasks  map[string]*memTask
	order  []string
	fields []states.FieldDef
	calls  []string
	next   int

	failCall          map[string]error // call-name prefix -> error returned
	createUnsupported bool
	ignoreDescWrites  bool
	plainOnly         bool // report only a plain description (no Markdown source)
	relistAddsField   string
}

func newMem(fields []states.FieldDef) *memClient {
	return &memClient{tasks: map[string]*memTask{}, fields: fields, failCall: map[string]error{}}
}

func (m *memClient) rec(format string, a ...interface{}) string {
	s := fmt.Sprintf(format, a...)
	m.calls = append(m.calls, s)
	return s
}

func (m *memClient) fail(call string) error {
	for p, e := range m.failCall {
		if strings.HasPrefix(call, p) {
			return e
		}
	}
	return nil
}

func (m *memClient) addTask(id, name, desc, status string, tags ...string) *memTask {
	t := &memTask{id: id, name: name, desc: desc, status: status, tags: tags, values: map[string]string{}}
	m.tasks[id] = t
	m.order = append(m.order, id)
	return t
}

func (m *memClient) view(t *memTask) client.Task {
	out := client.Task{ID: t.id, Name: t.name, Description: t.desc, Status: t.status, Tags: append([]string(nil), t.tags...), DescriptionSource: "markdown"}
	if t.desc == "" {
		out.DescriptionSource = "empty"
	}
	if m.plainOnly && t.desc != "" {
		out.DescriptionSource = "plain"
	}
	out.Fields = map[string]states.RemoteField{}
	for _, f := range m.fields {
		rf := states.RemoteField{ID: f.ID, Name: f.Name, Type: f.Type}
		if v, ok := t.values[f.ID]; ok {
			rf.Set = true
			if f.Type == "drop_down" {
				rf.OptionName = v
			} else {
				rf.Text = v
			}
		}
		out.Fields[f.ID] = rf
	}
	return out
}

func (m *memClient) ListTasks(_ context.Context, _ string, page int, _ int64) ([]client.Task, bool, error) {
	m.rec("ListTasks %d", page)
	if page > 0 {
		return nil, false, nil
	}
	var out []client.Task
	for _, id := range m.order {
		out = append(out, m.view(m.tasks[id]))
	}
	return out, false, nil
}

func (m *memClient) GetTask(_ context.Context, id string) (client.Task, error) {
	c := m.rec("GetTask %s", id)
	if e := m.fail(c); e != nil {
		return client.Task{}, e
	}
	t, ok := m.tasks[id]
	if !ok {
		return client.Task{}, errors.New("no such task")
	}
	return m.view(t), nil
}

func (m *memClient) CreateTask(_ context.Context, _ string, t client.Task) (client.Task, error) {
	c := m.rec("CreateTask %s", t.Name)
	if e := m.fail(c); e != nil {
		return client.Task{}, e
	}
	m.next++
	id := fmt.Sprintf("n%d", m.next)
	nt := m.addTask(id, t.Name, t.Description, t.Status)
	for _, tg := range t.Tags {
		nt.tags = append(nt.tags, strings.ToLower(tg))
	}
	return m.view(nt), nil
}

func (m *memClient) UpdateTask(_ context.Context, t client.Task) error {
	c := m.rec("UpdateTask %s name=%q desc=%v status=%q", t.ID, t.Name, t.Description != "", t.Status)
	if e := m.fail(c); e != nil {
		return e
	}
	mt := m.tasks[t.ID]
	if t.Name != "" {
		mt.name = t.Name
	}
	if t.Description != "" {
		mt.desc = t.Description
	}
	if t.Status != "" {
		mt.status = t.Status
	}
	return nil
}

func (m *memClient) SetCustomField(context.Context, string, string, string) error {
	return client.ErrNotImplemented
}

func (m *memClient) AddTag(_ context.Context, id, tag string) error {
	m.rec("AddTag %s %s", id, tag)
	m.tasks[id].tags = append(m.tasks[id].tags, strings.ToLower(tag))
	return nil
}

func (m *memClient) RemoveTag(_ context.Context, id, tag string) error {
	m.rec("RemoveTag %s %s", id, tag)
	var keep []string
	for _, t := range m.tasks[id].tags {
		if t != strings.ToLower(tag) {
			keep = append(keep, t)
		}
	}
	m.tasks[id].tags = keep
	return nil
}

func (m *memClient) DeleteTask(_ context.Context, id string) error {
	m.rec("DeleteTask %s", id)
	return nil
}

func (m *memClient) ListFields(_ context.Context, _ string) ([]states.FieldDef, error) {
	c := m.rec("ListFields")
	if e := m.fail(c); e != nil {
		return nil, e
	}
	out := append([]states.FieldDef(nil), m.fields...)
	if m.relistAddsField != "" && countPrefix(m.calls, "ListFields") >= 2 {
		out = append(out, states.FieldDef{ID: "late", Name: m.relistAddsField, Type: "text"})
	}
	return out, nil
}

func (m *memClient) CreateField(_ context.Context, _ string, def states.FieldDef) (states.FieldDef, error) {
	c := m.rec("CreateField %s", def.Name)
	if m.createUnsupported {
		return states.FieldDef{}, client.ErrFieldCreateUnsupported
	}
	if e := m.fail(c); e != nil {
		return states.FieldDef{}, e
	}
	d := states.FieldDef{ID: "cf-" + def.Name, Name: def.Name, Type: def.Type}
	for i, o := range def.Options {
		d.Options = append(d.Options, states.OptionDef{ID: fmt.Sprintf("%s-o%d", d.ID, i), Name: o.Name, OrderIndex: i})
	}
	m.fields = append(m.fields, d)
	return d, nil
}

func (m *memClient) SetFieldValue(_ context.Context, taskID, fieldID string, value interface{}) error {
	c := m.rec("SetFieldValue %s %s", taskID, fieldID)
	if e := m.fail(c); e != nil {
		return e
	}
	for _, f := range m.fields {
		if f.ID != fieldID {
			continue
		}
		if f.Type == "drop_down" {
			for _, o := range f.Options {
				if o.ID == value {
					m.tasks[taskID].values[fieldID] = o.Name
					return nil
				}
			}
			return errors.New("unknown option id")
		}
		m.tasks[taskID].values[fieldID] = value.(string)
		return nil
	}
	return errors.New("unknown field")
}

func (m *memClient) ClearFieldValue(_ context.Context, taskID, fieldID string) error {
	c := m.rec("ClearFieldValue %s %s", taskID, fieldID)
	if e := m.fail(c); e != nil {
		return e
	}
	delete(m.tasks[taskID].values, fieldID)
	return nil
}

func (m *memClient) SetDescription(_ context.Context, taskID, md string) error {
	c := m.rec("SetDescription %s", taskID)
	if e := m.fail(c); e != nil {
		return e
	}
	if !m.ignoreDescWrites {
		m.tasks[taskID].desc = md
	}
	return nil
}

func countPrefix(calls []string, p string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, p) {
			n++
		}
	}
	return n
}

func (m *memClient) writeCalls() []string {
	var out []string
	for _, c := range m.calls {
		if !strings.HasPrefix(c, "ListTasks") && !strings.HasPrefix(c, "GetTask") && !strings.HasPrefix(c, "ListFields") {
			out = append(out, c)
		}
	}
	return out
}

func (m *memClient) mark() int { return len(m.calls) }

func (m *memClient) writesSince(i int) []string {
	var out []string
	for _, c := range m.calls[i:] {
		if !strings.HasPrefix(c, "ListTasks") && !strings.HasPrefix(c, "GetTask") && !strings.HasPrefix(c, "ListFields") {
			out = append(out, c)
		}
	}
	return out
}

func ddField(spec *states.Spec, key string) states.FieldDef {
	f, _ := spec.Field(key)
	d := states.FieldDef{ID: "cf-" + key, Name: key, Type: "drop_down"}
	for i, v := range f.NonUnsetValues() {
		d.Options = append(d.Options, states.OptionDef{ID: fmt.Sprintf("%s-o%d", d.ID, i), Name: v, OrderIndex: i})
	}
	return d
}

func allFieldDefs(spec *states.Spec) []states.FieldDef {
	var out []states.FieldDef
	for _, f := range spec.Fields {
		if f.Kind == states.KindEnum {
			out = append(out, ddField(spec, f.Key))
		} else {
			out = append(out, states.FieldDef{ID: "cf-" + f.Key, Name: f.Key, Type: "text"})
		}
	}
	return out
}

// seedInSync adds a remote task that matches li on name/column/label.
func seedInSync(m *memClient, li mapper.LocalItem) *memTask {
	col, _ := mapper.StatusColumn(li.Status)
	lbl, _ := mapper.StatusLabel(li.Status)
	return m.addTask("r-"+li.Key, mapper.RemoteTitle(li), li.Description, col, strings.ToLower(li.Type), strings.ToLower(lbl))
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// planAndApply runs one full pass the way the CLI does.
func planAndApply(t *testing.T, m *memClient, local []mapper.LocalItem, sc *StatesContext, o ApplyOptions) (Plan, ApplyResult) {
	t.Helper()
	remote, err := FetchAllRemote(context.Background(), m, "L")
	if err != nil {
		t.Fatal(err)
	}
	plan := PlanReconcileWith(local, remote, PlanOptions{States: sc})
	o.States = sc
	res, err := ApplyWith(context.Background(), m, mapper.New(), "L", plan, local, o)
	if err != nil {
		t.Fatal(err)
	}
	return plan, res
}
