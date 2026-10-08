package hostpaths

import (
	"path/filepath"
	"testing"
)

func TestRootOfLogDir(t *testing.T) {
	tests := []struct {
		name     string
		dir      string
		wantRoot string
		wantOK   bool
	}{
		{"absolute", "/srv/repo/.spindrift/logs", "/srv/repo", true},
		{"absolute unclean", "/srv/repo/.spindrift/logs/", "/srv/repo", true},
		{"relative", ".spindrift/logs", ".", true},
		{"relative nested", "a/b/.spindrift/logs", "a/b", true},
		{"outbox sibling", "/srv/repo/.spindrift/outbox", "", false},
		{"plain dir", "/srv/repo/logs", "", false},
		{"too deep", "/srv/repo/.spindrift/logs/sub", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, ok := RootOfLogDir(tt.dir)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && root != filepath.Clean(tt.wantRoot) {
				t.Errorf("root = %q, want %q", root, tt.wantRoot)
			}
		})
	}
}
