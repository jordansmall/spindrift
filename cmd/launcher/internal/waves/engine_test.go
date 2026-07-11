package waves

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteBlockedMarker(t *testing.T) {
	pwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(pwd, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeBlockedMarker(pwd, []string{"11", "13"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(pwd, "logs", blockedMarker))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != "#11, #13" {
		t.Errorf("expected %q, got %q", "#11, #13", got)
	}
}
