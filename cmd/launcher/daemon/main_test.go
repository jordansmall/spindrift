package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/daemon"
)

func TestResolveKnob(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		doc        *inputDocument
		envVar     string
		want       string
		wantErr    bool
		wantWarned bool
	}{
		{
			name:   "document only",
			doc:    &inputDocument{Settings: map[string]string{"DAEMON_APP": ".#dogfood"}},
			envVar: "DAEMON_APP",
			want:   ".#dogfood",
		},
		{
			name:       "env overrides document, warns provenance",
			env:        map[string]string{"DAEMON_APP": ".#from-env"},
			doc:        &inputDocument{Settings: map[string]string{"DAEMON_APP": ".#from-doc"}},
			envVar:     "DAEMON_APP",
			want:       ".#from-env",
			wantWarned: true,
		},
		{
			// The document carries no value for this knob, so there is
			// nothing ambiguous about the env value winning — no warning.
			name:   "env only, no document value, no warning",
			env:    map[string]string{"DAEMON_APP": ".#from-env"},
			doc:    &inputDocument{Settings: map[string]string{}},
			envVar: "DAEMON_APP",
			want:   ".#from-env",
		},
		{
			name:    "missing knob errors",
			doc:     &inputDocument{Settings: map[string]string{}},
			envVar:  "DAEMON_APP",
			wantErr: true,
		},
		{
			// BASE_BRANCH is a real knob the host environment may also carry
			// (e.g. dogfood.sh's own BASE_BRANCH=main), so this clears it
			// explicitly rather than relying on it being ambiently absent.
			name:    "nil document, no env errors",
			env:     map[string]string{"BASE_BRANCH": ""},
			doc:     nil,
			envVar:  "BASE_BRANCH",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			var stderr bytes.Buffer
			got, err := resolveKnob(tt.doc, tt.envVar, &stderr)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveKnob() = %q, nil; want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveKnob() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("resolveKnob() = %q, want %q", got, tt.want)
			}
			warned := stderr.Len() > 0
			if warned != tt.wantWarned {
				t.Errorf("resolveKnob() stderr = %q, wantWarned %v", stderr.String(), tt.wantWarned)
			}
			if tt.wantWarned && !strings.Contains(stderr.String(), tt.envVar+"=") {
				t.Errorf("resolveKnob() warning %q missing %s=", stderr.String(), tt.envVar)
			}
		})
	}
}

// TestParseSlots pins MAX_PARALLEL's parse step: the schema declares it
// intKind = "positive" (lib/env-schema.nix), so anything else — unparsable,
// zero, or negative — must fail startup with a clear diagnostic rather than
// reach daemon.Loop's own non-positive-Slots halt, which is for a
// programming error, not an operator's env var.
func TestParseSlots(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int
		wantErr bool
	}{
		{name: "positive integer", raw: "3", want: 3},
		{name: "one", raw: "1", want: 1},
		{name: "zero rejected", raw: "0", wantErr: true},
		{name: "negative rejected", raw: "-1", wantErr: true},
		{name: "non-numeric rejected", raw: "many", wantErr: true},
		{name: "empty rejected", raw: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSlots(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseSlots(%q) = %d, nil; want an error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSlots(%q) unexpected error: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("parseSlots(%q) = %d, want %d", tt.raw, got, tt.want)
			}
		})
	}
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantPath string
		wantKind daemon.Kind
		wantErr  bool
	}{
		{
			name:     "default kind is dispatch",
			args:     []string{"--input", "/tmp/in.json"},
			wantPath: "/tmp/in.json",
			wantKind: daemon.KindDispatch,
		},
		{
			name:     "explicit research",
			args:     []string{"--input", "/tmp/in.json", "research"},
			wantPath: "/tmp/in.json",
			wantKind: daemon.KindResearch,
		},
		{
			name:    "unknown kind rejected",
			args:    []string{"--input", "/tmp/in.json", "bogus"},
			wantErr: true,
		},
		{
			name:    "missing --input",
			args:    []string{"dispatch"},
			wantErr: true,
		},
		{
			name:    "--input with no value",
			args:    []string{"--input"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseArgs(%v) = %+v, nil; want an error", tt.args, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseArgs(%v) unexpected error: %v", tt.args, err)
			}
			if got.InputPath != tt.wantPath || got.Kind != tt.wantKind {
				t.Errorf("parseArgs(%v) = %+v, want {InputPath:%q Kind:%q}", tt.args, got, tt.wantPath, tt.wantKind)
			}
		})
	}
}

func TestIsOperatorStop(t *testing.T) {
	tests := []struct {
		reason string
		want   bool
	}{
		{"context-cancelled: context canceled", true},
		{"outcome: signalled-stop", true},
		{"outcome: host-tainted", false},
		{"outcome: config-invalid", false},
		{"resolve-revision: boom", false},
		{"run-child: boom", false},
	}
	for _, tt := range tests {
		if got := isOperatorStop(tt.reason); got != tt.want {
			t.Errorf("isOperatorStop(%q) = %v, want %v", tt.reason, got, tt.want)
		}
	}
}

// TestHandleStopSignal asserts one signal cancels ctx and invokes forward
// exactly once — the daemon's signal-wiring goroutine, extracted out of main
// so the halt path is exercised without sending the test process a real
// signal (issue #3538).
func TestHandleStopSignal(t *testing.T) {
	sig := make(chan os.Signal, 1)
	ctx, cancel := context.WithCancel(context.Background())

	var mu sync.Mutex
	forwardCalls := 0
	forward := func() {
		mu.Lock()
		forwardCalls++
		mu.Unlock()
	}

	done := make(chan struct{})
	go func() {
		handleStopSignal(sig, cancel, forward)
		close(done)
	}()

	sig <- syscall.SIGTERM

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleStopSignal did not return after a signal")
	}

	if ctx.Err() == nil {
		t.Error("ctx.Err() = nil, want context cancelled")
	}
	mu.Lock()
	got := forwardCalls
	mu.Unlock()
	if got != 1 {
		t.Errorf("forward called %d times, want 1", got)
	}
}

// TestMainRun_BadArgv drives mainRun's own startup path (rather than
// os.Exit'ing main() directly, which a test process cannot observe) with an
// argv that fails parseArgs, asserting the exit code and message land on the
// injected stderr rather than the real os.Stderr.
func TestMainRun_BadArgv(t *testing.T) {
	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"dispatch"}, &stdout, &stderr)
	if got != 1 {
		t.Errorf("mainRun() = %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "daemon:") {
		t.Errorf("stderr = %q, want a daemon: prefixed message", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

// TestMainRun_MissingInputDocument exercises the second early-return path: a
// well-formed argv pointing at an --input path that does not exist.
func TestMainRun_MissingInputDocument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")
	got := mainRun([]string{"--input", missing}, &stdout, &stderr)
	if got != 1 {
		t.Errorf("mainRun() = %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "read input document") {
		t.Errorf("stderr = %q, want it to mention the unreadable input document", stderr.String())
	}
}

// TestRepoRoot_NotAGitCheckout asserts repoRoot fails fast (rather than
// handing back a subdirectory path that only breaks the first child) when
// dir is not inside any git checkout.
func TestRepoRoot_NotAGitCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	if _, err := repoRoot(dir); err == nil {
		t.Error("repoRoot() error = nil, want an error for a non-git directory")
	}
}

// TestRepoRoot_ResolvesToplevel asserts repoRoot run from a subdirectory of
// a checkout resolves to the checkout's root, not the subdirectory itself.
func TestRepoRoot_ResolvesToplevel(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	gitRunT(t, root, "-c", "init.defaultBranch=main", "init")
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := repoRoot(sub)
	if err != nil {
		t.Fatalf("repoRoot() unexpected error: %v", err)
	}
	// Resolve both sides through EvalSymlinks: on macOS t.TempDir() lives
	// under a /var symlink to /private/var, and git's --show-toplevel
	// resolves it while root here does not.
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks(root): %v", err)
	}
	gotRoot, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("EvalSymlinks(got): %v", err)
	}
	if gotRoot != wantRoot {
		t.Errorf("repoRoot(%q) = %q, want %q", sub, got, root)
	}
}
