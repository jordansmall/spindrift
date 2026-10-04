package claude

import (
	"os"
	"path/filepath"
	"testing"
)

const sessionTestID = "bf718c96-aa0c-c4fa-d029-3a6bbb980c63"

func TestSessionFlags(t *testing.T) {
	home := t.TempDir()
	if got := SessionFlags("initial", "owner/repo", "7", home); got != "--session-id "+sessionTestID {
		t.Errorf("initial = %q", got)
	}
	if got := SessionFlags("resume", "owner/repo", "7", home); got != "" {
		t.Errorf("resume without transcript = %q; want empty", got)
	}
	proj := filepath.Join(home, ".claude", "projects", "x")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, sessionTestID+".jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := SessionFlags("resume", "owner/repo", "7", home); got != "--resume "+sessionTestID {
		t.Errorf("resume with transcript = %q", got)
	}
	if got := SessionFlags("resume", "other/repo", "7", home); got != "" {
		t.Errorf("resume for another repo = %q; want empty", got)
	}
	for _, mode := range []string{"", "bogus"} {
		if got := SessionFlags(mode, "owner/repo", "7", home); got != "" {
			t.Errorf("mode %q = %q; want empty", mode, got)
		}
	}
}
