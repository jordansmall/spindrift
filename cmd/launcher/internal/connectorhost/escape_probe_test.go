//go:build escapeprobe

package connectorhost_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
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

func TestEscapeWritesAndRemovesAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe")
	if got := escape(t, "write", path); !got.OK {
		t.Errorf("write = %+v, want ok", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("stat after write = %v, want the probe file removed", err)
	}
	if got := escape(t, "write", filepath.Join(t.TempDir(), "absent", "probe")); got.OK || got.Detail == "" {
		t.Errorf("write under a missing directory = %+v, want failure with a reason", got)
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

// escapeThrough loads escape.wasm into s and runs one probe call.
func escapeThrough(t *testing.T, s *connectorhost.Sessions, kind, arg string) error {
	t.Helper()
	c, err := s.Load(context.Background(), fixture(t, "escape.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	return c.Call(context.Background(), "escape", map[string]string{"kind": kind, "arg": arg}, new(escapeResult))
}

// The frame probe writes onto the host's stream as a session the Launcher never
// issued, which fails the whole pipe.
func TestEscapeFrameForgesAChunkForAnotherSession(t *testing.T) {
	s := sessionsHost(t)
	err := escapeThrough(t, s, "frame", "2 http://backend.invalid/x")
	if !errors.Is(err, connectorhost.ErrMalformedOutput) {
		t.Errorf("forged frame for session 2: err = %v, want ErrMalformedOutput", err)
	}
	if !errors.Is(s.Dead(), connectorhost.ErrMalformedOutput) {
		t.Errorf("Dead = %v, want the pipe failed with ErrMalformedOutput", s.Dead())
	}
}

// Under its own session id the frame is a well-formed http_request, which a
// session loaded without a Host grant refuses.
func TestEscapeFrameForgesAnHTTPRequestUnderItsOwnSession(t *testing.T) {
	s := sessionsHost(t)
	err := escapeThrough(t, s, "frame", "1 http://backend.invalid/x")
	if !errors.Is(err, connectorhost.ErrMalformedOutput) || !strings.Contains(err.Error(), "http_request without a grant") {
		t.Errorf("forged frame for own session: err = %v, want the grantless http_request refused", err)
	}
}

func TestEscapeFrameOutsideAServeSessionsFailsCleanly(t *testing.T) {
	if got := escape(t, "frame", "1 http://backend.invalid/x"); got.OK || got.Detail == "" {
		t.Errorf("frame outside ServeSessions = %+v, want failure with a reason", got)
	}
}
