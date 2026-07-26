// Package mapper translates between the local workable-items model and the
// remote task model.
//
// MINIMUM-VIABLE MODE (title-prefix keying): the live board carries the item
// key as a `[ATM-NNN]` TITLE prefix (no custom fields), so the mapper matches +
// builds tasks around that convention (see keys.go). The fields mapped in this
// mode are Status (status.go vocab), Type (a ClickUp tag), title and
// description — the subset a real dry-run reconcile + gated push needs. The
// full 17-field custom-field map (P2 §1) is a later phase and is NOT required
// while the board has zero custom fields.
package mapper

import "strings"

// LocalItem is the engine-internal view of a workable-items row. It is a SUBSET
// of the consumer's §11.4.93 schema — task_bridge depends only on these neutral
// fields, never on a project-specific column. The consumer's DB layer fills it.
type LocalItem struct {
	Key          string   // value of the consumer's item-key field (e.g. ATM-013)
	Title        string   // items.title (heading text)
	Description  string   // items.description
	Type         string   // Bug/Feature/Task (consumer closed-set)
	Status       string   // consumer closed-set + Deleted
	Severity     string   // free text (Critical/High/...)
	Location     string   // Issues/Fixed (current_location) — informational
	VersionTags  []string // opened-in/fixed-in tags (reused, not re-derived)
	CreatedBy    string   // canonical handle
	AssignedTo   string   // canonical handle
	LastModified string   // local last_modified (compared, not pushed)
}

// Mapper converts both directions.
type Mapper interface {
	// ToRemote builds the remote task representation for a create/update push.
	ToRemote(it LocalItem) (RemoteTask, error)
	// ToLocal builds the (partial) local item from a pulled remote task.
	ToLocal(rt RemoteTask) (LocalItem, error)
}

// RemoteTask is the mapper's neutral remote view (decoupled from go-clickup's
// concrete struct so the transport can be swapped without touching the mapper).
type RemoteTask struct {
	ID            string
	Name          string // includes the `[KEY]` title prefix
	Description   string
	Status        string   // ClickUp status NAME
	Tags          []string // includes the Type tag
	DateUpdatedMS int64
	CustomFields  map[string]string
}

type titlePrefixMapper struct{}

// New returns the minimum-viable title-prefix mapper.
func New() Mapper { return titlePrefixMapper{} }

// ToRemote builds the ClickUp-facing task for a push (create or update). The
// key is embedded as the title prefix (the board convention); Status is mapped
// through the §3 vocab; Type becomes a ClickUp tag.
func (titlePrefixMapper) ToRemote(it LocalItem) (RemoteTask, error) {
	if it.Key == "" {
		return RemoteTask{}, ErrMissingKey
	}
	status, ok := StatusToRemote(it.Status)
	if !ok {
		return RemoteTask{}, ErrUnmappedStatus
	}
	rt := RemoteTask{
		Name:        TitleWithKey(it.Key, cleanTitle(it.Key, it.Title)),
		Description: it.Description,
		Status:      status,
	}
	if t := strings.TrimSpace(it.Type); t != "" {
		rt.Tags = []string{t}
	}
	return rt, nil
}

// ToLocal recovers the key + status from a pulled remote task. It fills only
// the fields this mode round-trips; unmapped remote statuses surface as an
// error (never a guessed local value, §11.4.6 / P2 §3 DZ-9).
func (titlePrefixMapper) ToLocal(rt RemoteTask) (LocalItem, error) {
	key, ok := ParseKey(rt.Name)
	if !ok {
		return LocalItem{}, ErrNoKeyInTitle
	}
	status, ok := StatusToLocal(rt.Status)
	if !ok {
		return LocalItem{}, ErrUnmappedStatus
	}
	return LocalItem{
		Key:         key,
		Title:       cleanTitle(key, rt.Name),
		Description: rt.Description,
		Status:      status,
	}, nil
}

// cleanTitle strips a leading `[KEY] ` prefix from a title so the stored title
// is prefix-free (the prefix is re-applied by TitleWithKey on push).
func cleanTitle(key, title string) string {
	prefix := "[" + key + "]"
	if strings.HasPrefix(title, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(title, prefix))
	}
	return title
}
