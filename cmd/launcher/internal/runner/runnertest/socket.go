package runnertest

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// NewTestSocket deliberately avoids t.TempDir(): that helper's directory embeds
// the full test name, and under a nix build sandbox the build root is already
// long, so the two together can exceed AF_UNIX's ~108-byte sun_path limit
// (net.Listen then fails with "bind: invalid argument"). A short os.MkdirTemp
// prefix keeps the path under the limit whatever the test name.
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
