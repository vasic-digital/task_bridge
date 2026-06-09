// Package resolver turns ClickUp board/folder URLs into folder/list IDs.
//
// P1 SCAFFOLD: interface + stub. P0 §1.6 resolution strategy (no guessing per
// §11.4.6): the ClickUp app-URL segment grammar is UNCONFIRMED, so the real
// resolver (P2.2) extracts EVERY numeric segment from the URL and validates each
// candidate against the live API (GET /folder/{id}, GET /list/{id}), keeping the
// one that resolves — turning an unproven URL grammar into a proven ID via a
// real probe. This file declares the contract; the probing logic lands in P2.
package resolver

import "context"

// Resolved holds the IDs derived (and API-validated) from the injected URLs.
type Resolved struct {
	FolderID string
	ListID   string
}

// Resolver derives IDs from URLs. Generic: it knows nothing about any specific
// consumer's board — it receives the URLs from config.Config at runtime.
type Resolver interface {
	// Resolve extracts numeric candidates from folderURL/boardURL and validates
	// each against the API, returning the proven IDs (P2.2).
	Resolve(ctx context.Context, folderURL, boardURL string) (Resolved, error)
}

type stubResolver struct{}

// New returns the P1 stub resolver.
func New() Resolver { return stubResolver{} }

func (stubResolver) Resolve(context.Context, string, string) (Resolved, error) {
	return Resolved{}, ErrNotImplemented
}
