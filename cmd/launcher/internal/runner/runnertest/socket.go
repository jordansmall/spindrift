package runnertest

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// NewTestSocket avoids t.TempDir(), whose path embeds the test name: under a
// nix build sandbox's long root that can exceed AF_UNIX's ~108-byte sun_path
// limit ("bind: invalid argument").
func NewTestSocket(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sock-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, name)
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("net.Listen(unix, %s): %v", path, err)
	}
	t.Cleanup(func() { l.Close() })
	return path
}
