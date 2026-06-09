// Package mapper translates between the local workable-items model and the
// remote client.Task model (P0 §5.3 field mapping).
//
// P1 SCAFFOLD: the LocalItem type + Mapper interface + stub. The real two-call
// custom-field write (Update-Task for name/desc/status/tags + per-field
// Set-Custom-Field), the Type/Severity/version-tag mapping, and the user map
// land in P3. The item key is the consumer-injected ItemKeyCustomField (generic).
package mapper

// LocalItem is the engine-internal view of a workable-items row. It is a SUBSET
// of the consumer's schema — task_bridge depends only on these neutral fields,
// never on a project-specific column. The consumer's DB layer fills it in.
type LocalItem struct {
	Key          string   // value of the consumer's item-key field (e.g. ATM-123)
	Title        string
	Description  string
	Type         string   // Bug/Feature/Task (consumer closed-set)
	Status       string   // consumer closed-set + Deleted
	Severity     string
	VersionTags  []string // opened-in/fixed-in tags (reused, not re-derived)
	CreatedBy    string   // canonical handle
	AssignedTo   string   // canonical handle
	LastModified string   // local last_modified (compared, not pushed)
}

// Mapper converts both directions. itemKeyField names the custom field that
// carries LocalItem.Key on the remote side (injected — generic).
type Mapper interface {
	// ToRemote builds the remote task representation for a push.
	ToRemote(it LocalItem, itemKeyField string) (RemoteTask, error)
	// ToLocal builds the local item from a pulled remote task.
	ToLocal(rt RemoteTask, itemKeyField string) (LocalItem, error)
}

// RemoteTask is the mapper's neutral remote view (decoupled from go-clickup's
// concrete struct so the transport can be swapped without touching the mapper).
type RemoteTask struct {
	ID            string
	Name          string
	Description   string
	Status        string
	Tags          []string
	DateUpdatedMS int64
	CustomFields  map[string]string
}

type stubMapper struct{}

// New returns the P1 stub mapper.
func New() Mapper { return stubMapper{} }

func (stubMapper) ToRemote(LocalItem, string) (RemoteTask, error) {
	return RemoteTask{}, ErrNotImplemented
}
func (stubMapper) ToLocal(RemoteTask, string) (LocalItem, error) {
	return LocalItem{}, ErrNotImplemented
}
