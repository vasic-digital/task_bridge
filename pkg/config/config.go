// Package config defines the runtime configuration contract for task_bridge.
//
// DECOUPLING CONTRACT (constitution §11.4.28): task_bridge is a generic,
// project-agnostic sync engine. It MUST contain ZERO knowledge of any specific
// consuming project — no hardcoded credentials, board IDs, folder IDs, package
// names, hostnames, regions, or asset names. EVERYTHING project-specific is
// injected by the consumer at runtime through the Config struct below:
//
//   - credentials  -> read by the consumer from its own .env / secret store and
//                     passed in via Config.APIToken (the value is NEVER logged,
//                     NEVER persisted by this engine — §11.4.10).
//   - board/folder -> the consumer passes the ClickUp folder/board URLs (or
//                     pre-resolved IDs) via Config.FolderURL / Config.BoardURL;
//                     pkg/resolver turns URLs into IDs by probing the API.
//   - item key     -> Config.ItemKeyCustomField (the ATM_ID-style custom field
//                     used as the immutable cross-system key).
//
// A consumer wires task_bridge by constructing a Config and calling the engine;
// the engine reaches back into the consumer ONLY through this struct.
package config

import "time"

// DeleteBehavior governs what the engine does when an item is marked Deleted
// on the LOCAL side. NeverAutoDeleteRemote is the safe default (constitution
// §9 data-safety + §11.4.122): the engine never destroys remote data on its own.
type DeleteBehavior string

const (
	// NeverAutoDeleteRemote: a local Deleted marks the item Deleted in the
	// tracker docs/DB and (optionally) sets a Deleted status on the remote,
	// but NEVER issues a remote DELETE. This is the operator-decided default.
	NeverAutoDeleteRemote DeleteBehavior = "never-auto-delete-remote"
	// AllowRemoteDelete: only when the consumer opts in explicitly. Destructive.
	AllowRemoteDelete DeleteBehavior = "allow-remote-delete"
)

// Config is the complete runtime injection point. A consuming project builds
// this from its OWN configuration (env vars, secret store, board URLs) and
// hands it to the engine. No field here is ever defaulted to a project-specific
// value inside this package.
type Config struct {
	// --- credentials (injected; never logged, never persisted by the engine) ---
	// APIToken is the ClickUp personal API token (pk_...). The consumer reads it
	// from its own .env (e.g. CLICKUP_API_KEY) and passes it here. §11.4.10.
	APIToken string

	// --- board/folder identity (injected as URLs or pre-resolved IDs) ---
	FolderURL string // e.g. consumer's CLICKUP_FOLDER url; resolved to FolderID
	BoardURL  string // e.g. consumer's CLICKUP_BOARD url; resolved to ListID
	FolderID  string // optional: pre-resolved, skips URL probing
	ListID    string // optional: pre-resolved, skips URL probing

	// --- item-key contract ---
	// ItemKeyCustomField is the ClickUp custom-field name used as the immutable
	// cross-system key (operator decision: "ATM_ID"). Generic — any consumer
	// supplies its own key field name.
	ItemKeyCustomField string

	// --- sync behaviour ---
	DeleteBehavior   DeleteBehavior // operator default: NeverAutoDeleteRemote
	ReconcileEvery   time.Duration  // operator default: 10 * time.Minute
	DryRun           bool           // default true — never pollute the board

	// --- local SSoT (injected path; engine never assumes a location) ---
	// DBPath is the consumer's workable-items SQLite DB file. The engine reads
	// and writes this DB but never assumes where it lives.
	DBPath string

	// --- user mapping (injected) ---
	// UserMap maps ClickUp user-id -> canonical handle (consumer-owned).
	UserMap map[string]string
}

// Defaults returns a Config pre-populated with the engine's safe, generic
// defaults (NOT project-specific values). The consumer overlays its injected
// fields on top. These defaults encode the operator-confirmed P0 decisions.
func Defaults() Config {
	return Config{
		DeleteBehavior: NeverAutoDeleteRemote, // §9 safe default
		ReconcileEvery: 10 * time.Minute,      // operator-decided cadence
		DryRun:         true,                   // never pollute the board by default
	}
}

// Validate checks that the consumer supplied the minimum required injection.
// It deliberately does NOT log the token value (§11.4.10).
func (c Config) Validate() error {
	if c.APIToken == "" {
		return ErrMissingToken
	}
	if c.ListID == "" && c.BoardURL == "" {
		return ErrMissingBoard
	}
	if c.DBPath == "" {
		return ErrMissingDB
	}
	if c.ItemKeyCustomField == "" {
		return ErrMissingItemKey
	}
	return nil
}
