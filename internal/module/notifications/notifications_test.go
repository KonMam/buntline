package notifications

import "testing"

// TestModuleInfo pins the store-facing identity: the modules page renders
// this card, and the browser gates its notification stream on it, so the
// id and default state must not drift silently.
func TestModuleInfo(t *testing.T) {
	m := Module{}
	info := m.Info()
	if info.ID != "notifications" {
		t.Fatalf("id = %q, want notifications", info.ID)
	}
	if !info.Default {
		t.Fatal("notifications should default to enabled")
	}
	if info.Name == "" || info.Description == "" {
		t.Fatal("name and description must be set")
	}
	if info.Core {
		t.Fatal("notifications is a toggleable feature, not core")
	}
}
