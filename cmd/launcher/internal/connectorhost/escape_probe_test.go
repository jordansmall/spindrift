//go:build escapeprobe

package connectorhost_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type escapeResult struct {
	OK        bool   `json:"ok"`
	Detail    string `json:"detail"`
	Truncated bool   `json:"truncated"`
}

func escape(t *testing.T, kind, arg string) escapeResult {
	t.Helper()
	c, err := load(t, "escape.wasm")
	if err != nil {
		t.Fatal(err)
	}
	var out escapeResult
	if err := c.Call(context.Background(), "escape", map[string]string{"kind": kind, "arg": arg}, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEscapeReadsFileAndDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(path, []byte("hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := escape(t, "read", path); !got.OK || !strings.Contains(got.Detail, "hunter2") {
		t.Errorf("read file = %+v, want ok with contents", got)
	}
	if got := escape(t, "read", dir); !got.OK || !strings.Contains(got.Detail, "secret.txt") {
		t.Errorf("read dir = %+v, want ok listing secret.txt", got)
	}
	if got := escape(t, "read", filepath.Join(dir, "absent")); got.OK || got.Detail == "" {
		t.Errorf("read absent = %+v, want failure with a reason", got)
	}
}

func TestEscapeDialsTCPAndUnix(t *testing.T) {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	sock := filepath.Join(t.TempDir(), "s")
	unix, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close()
	if got := escape(t, "tcp", tcp.Addr().String()); !got.OK {
		t.Errorf("tcp = %+v, want ok", got)
	}
	if got := escape(t, "unix", sock); !got.OK {
		t.Errorf("unix = %+v, want ok", got)
	}
	tcp.Close()
	if got := escape(t, "tcp", tcp.Addr().String()); got.OK || got.Detail == "" {
		t.Errorf("tcp to closed port = %+v, want failure with a reason", got)
	}
}

func TestEscapeReadsHostEnvironment(t *testing.T) {
	t.Setenv("SPINDRIFT_ESCAPE_CANARY", "leaked")
	if got := escape(t, "env", ""); !got.OK || !strings.Contains(got.Detail, "SPINDRIFT_ESCAPE_CANARY=leaked") {
		t.Errorf("env = %+v, want ok holding the canary", got)
	}
}

func TestEscapeUnknownKindFailsCleanly(t *testing.T) {
	if got := escape(t, "bogus", ""); got.OK || got.Detail == "" {
		t.Errorf("unknown kind = %+v, want failure with a reason", got)
	}
}
