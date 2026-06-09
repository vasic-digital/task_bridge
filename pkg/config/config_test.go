package config

import (
	"testing"
	"time"
)

// TestDefaultsEncodeOperatorDecisions verifies the P0/operator-confirmed
// defaults are baked into the engine (generic, not project-specific values):
// never-auto-delete-remote, 10-min cadence, dry-run ON.
func TestDefaultsEncodeOperatorDecisions(t *testing.T) {
	d := Defaults()
	if d.DeleteBehavior != NeverAutoDeleteRemote {
		t.Fatalf("DeleteBehavior = %q, want %q", d.DeleteBehavior, NeverAutoDeleteRemote)
	}
	if d.ReconcileEvery != 10*time.Minute {
		t.Fatalf("ReconcileEvery = %v, want 10m", d.ReconcileEvery)
	}
	if !d.DryRun {
		t.Fatal("DryRun must default to true (never pollute the board)")
	}
}

// TestValidateNamesMissingInjectionWithoutLeakingValues confirms Validate
// reports the missing consumer-injected field and that a populated token does
// not appear in any error string (§11.4.10 — no credential leak).
func TestValidateMissingInjection(t *testing.T) {
	if err := (Config{}).Validate(); err != ErrMissingToken {
		t.Fatalf("empty config: got %v, want ErrMissingToken", err)
	}
	c := Defaults()
	c.APIToken = "pk_secret_value_must_not_leak"
	c.BoardURL = "https://example/board"
	c.DBPath = "/tmp/x.db"
	c.ItemKeyCustomField = "ATM_ID"
	if err := c.Validate(); err != nil {
		t.Fatalf("fully injected config should validate, got %v", err)
	}
	// negative: the token value must never be embedded in a returned error.
	c.ItemKeyCustomField = ""
	if err := c.Validate(); err == nil {
		t.Fatal("expected ErrMissingItemKey")
	} else if got := err.Error(); containsSecret(got) {
		t.Fatalf("error string leaked the token: %q", got)
	}
}

func containsSecret(s string) bool {
	const secret = "pk_secret_value_must_not_leak"
	for i := 0; i+len(secret) <= len(s); i++ {
		if s[i:i+len(secret)] == secret {
			return true
		}
	}
	return false
}
