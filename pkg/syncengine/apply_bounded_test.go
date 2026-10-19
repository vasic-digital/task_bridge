package syncengine

import (
	"context"
	"errors"
	"testing"

	"github.com/vasic-digital/task_bridge/pkg/client"
	"github.com/vasic-digital/task_bridge/pkg/mapper"
)

// idClient wraps mockClient so CreateTask returns a deterministic task id and
// can fail for chosen titles.
type idClient struct {
	*mockClient
	failName string
}

func (c *idClient) CreateTask(ctx context.Context, listID string, t client.Task) (client.Task, error) {
	c.createReq = append(c.createReq, t)
	if c.failName != "" && t.Name == c.failName {
		return client.Task{}, errors.New("boom")
	}
	t.ID = "id-" + t.Name
	return t, nil
}

func createPlan(keys ...string) ([]mapper.LocalItem, Plan) {
	var local []mapper.LocalItem
	for _, k := range keys {
		local = append(local, mapper.LocalItem{Key: k, Status: "Queued", Title: "t " + k})
	}
	return local, PlanReconcile(local, nil)
}

// TestApplyWithLimitAndNewestFirst: Limit bounds the batch, NewestFirst takes
// the highest key numbers first, Remaining reports the rest, ids are recorded.
func TestApplyWithLimitAndNewestFirst(t *testing.T) {
	local, plan := createPlan("ATM-1001", "ATM-1325", "ATM-1244", "ATM-1300")
	mc := &idClient{mockClient: &mockClient{}}
	var events []ApplyEvent
	res, err := ApplyWith(context.Background(), mc, mapper.New(), "L", plan, local,
		ApplyOptions{Limit: 2, NewestFirst: true, OnEvent: func(e ApplyEvent) { events = append(events, e) }})
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 2 || res.Attempted != 2 || res.Remaining != 2 || res.Stopped {
		t.Fatalf("res = %+v, want created=2 attempted=2 remaining=2", res)
	}
	if len(mc.createReq) != 2 || mc.createReq[0].Name != "[ATM-1325] t ATM-1325" || mc.createReq[1].Name != "[ATM-1300] t ATM-1300" {
		t.Fatalf("create order = %v, want ATM-1325 then ATM-1300", mc.createReq)
	}
	if res.CreatedIDs["ATM-1325"] != "id-[ATM-1325] t ATM-1325" || len(res.CreatedIDs) != 2 {
		t.Fatalf("CreatedIDs = %v", res.CreatedIDs)
	}
	if len(events) != 2 || !events[0].OK || events[0].Op != "CREATE" || events[0].TaskID == "" {
		t.Fatalf("events = %+v", events)
	}
}

// TestApplyWithStopOnError: the batch halts at the FIRST failing item; later
// items are not attempted and are reported as remaining.
func TestApplyWithStopOnError(t *testing.T) {
	local, plan := createPlan("ATM-1", "ATM-2", "ATM-3")
	mc := &idClient{mockClient: &mockClient{}, failName: "[ATM-2] t ATM-2"}
	res, err := ApplyWith(context.Background(), mc, mapper.New(), "L", plan, local,
		ApplyOptions{StopOnError: true, NewestFirst: false})
	if err != nil {
		t.Fatal(err)
	}
	// legacy order for creates is key-sorted ascending: ATM-1 ok, ATM-2 fails, stop.
	if !res.Stopped || res.Created != 1 || len(res.Errors) != 1 || res.Remaining != 1 {
		t.Fatalf("res = %+v, want stopped, created=1, 1 error, remaining=1", res)
	}
	if len(mc.createReq) != 2 {
		t.Fatalf("create attempts = %d, want 2 (ATM-3 must NOT be attempted)", len(mc.createReq))
	}
}

// TestApplyWithoutStopOnErrorContinues is the control: the legacy behaviour
// (collect and continue) is unchanged when StopOnError is false.
func TestApplyWithoutStopOnErrorContinues(t *testing.T) {
	local, plan := createPlan("ATM-1", "ATM-2", "ATM-3")
	mc := &idClient{mockClient: &mockClient{}, failName: "[ATM-2] t ATM-2"}
	res, _ := Apply(context.Background(), mc, mapper.New(), "L", plan, local)
	if res.Stopped || res.Created != 2 || len(res.Errors) != 1 || len(mc.createReq) != 3 {
		t.Fatalf("res = %+v attempts=%d, want created=2 errors=1 attempts=3", res, len(mc.createReq))
	}
}

// TestTitleOnlyDriftGetsNameOnlyUpdate: a matched task whose column + label are
// right but whose title differs is flagged (not in sync) and repaired with a
// NAME-ONLY UpdateTask; no tag/column calls; an identical-title task is skipped.
func TestTitleOnlyDriftGetsNameOnlyUpdate(t *testing.T) {
	local := []mapper.LocalItem{
		{Key: "ATM-10", Status: "Queued", Title: "full new title"},
		{Key: "ATM-11", Status: "Queued", Title: "same"},
	}
	remote := []client.Task{
		{ID: "r10", Name: "[ATM-10] truncated ti", Status: "to do", Tags: []string{"status:queued"}},
		{ID: "r11", Name: "[ATM-11] same", Status: "to do", Tags: []string{"status:queued"}},
	}
	plan := PlanReconcile(local, remote)
	var drift, insync int
	for _, e := range plan.Update {
		if e.TitleDrift && !e.InSync {
			drift++
		}
		if e.InSync {
			insync++
		}
	}
	if drift != 1 || insync != 1 {
		t.Fatalf("plan = %+v, want 1 title-drift + 1 in-sync", plan.Update)
	}
	mc := &mockClient{}
	res, _ := Apply(context.Background(), mc, mapper.New(), "L", plan, local)
	if res.Updated != 1 || len(mc.updateReq) != 1 {
		t.Fatalf("res=%+v updates=%v", res, mc.updateReq)
	}
	u := mc.updateReq[0]
	if u.ID != "r10" || u.Name != "[ATM-10] full new title" || u.Description != "" || u.Status != "" || len(u.Tags) != 0 {
		t.Fatalf("title update = %+v, want NAME-ONLY update of r10", u)
	}
	if len(mc.addTagReq) != 0 || len(mc.remTagReq) != 0 || len(mc.deleteReq) != 0 {
		t.Fatalf("unexpected tag/delete calls: add=%v rem=%v del=%v", mc.addTagReq, mc.remTagReq, mc.deleteReq)
	}
	if res.UpdatedIDs["ATM-10"] != "r10" {
		t.Fatalf("UpdatedIDs = %v", res.UpdatedIDs)
	}
}
