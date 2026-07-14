package waves

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestWriteBlockedMarker verifies the marker annotates each blocker with the
// source (native relationship vs body-text parsing) it was resolved from,
// so the workflow's release comment — which interpolates this file's
// contents verbatim — carries the same annotation without needing its own
// source-formatting logic.
func TestWriteBlockedMarker(t *testing.T) {
	pwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(pwd, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	sources := map[string]forge.DepSource{"11": forge.DepSourceNative, "13": forge.DepSourceBody}
	if err := writeBlockedMarker(pwd, []string{"11", "13"}, sources); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(pwd, "logs", blockedMarker))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != "#11 (native), #13 (body)" {
		t.Errorf("expected %q, got %q", "#11 (native), #13 (body)", got)
	}
}
