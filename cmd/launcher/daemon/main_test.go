package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
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

// TestResolveKnobOptional pins the absent-is-normal shape DAEMON_AWAKE_WINDOW
// needs: an absent knob resolves to "" without an error and without a
// diagnostic, while the ambient-env-wins provenance warning (lookupKnob's
// whole reason for existing) still fires when the document also carries a
// value.
func TestResolveKnobOptional(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		doc        *inputDocument
		envVar     string
		want       string
		wantWarned bool
	}{
		{
			name:   "absent from both is not an error",
			doc:    &inputDocument{Settings: map[string]string{}},
			envVar: "DAEMON_AWAKE_WINDOW",
			want:   "",
		},
		{
			name:   "nil document, no env",
			doc:    nil,
			envVar: "DAEMON_AWAKE_WINDOW",
			want:   "",
		},
		{
			name:   "document only",
			doc:    &inputDocument{Settings: map[string]string{"DAEMON_AWAKE_WINDOW": "22:00-06:00 Europe/London"}},
			envVar: "DAEMON_AWAKE_WINDOW",
			want:   "22:00-06:00 Europe/London",
		},
		{
			name:       "env overrides document, warns provenance",
			env:        map[string]string{"DAEMON_AWAKE_WINDOW": "20:00-05:00 UTC"},
			doc:        &inputDocument{Settings: map[string]string{"DAEMON_AWAKE_WINDOW": "22:00-06:00 Europe/London"}},
			envVar:     "DAEMON_AWAKE_WINDOW",
			want:       "20:00-05:00 UTC",
			wantWarned: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			var stderr bytes.Buffer
			got := resolveKnobOptional(tt.doc, tt.envVar, &stderr)
			if got != tt.want {
				t.Errorf("resolveKnobOptional() = %q, want %q", got, tt.want)
			}
			warned := stderr.Len() > 0
			if warned != tt.wantWarned {
				t.Errorf("resolveKnobOptional() stderr = %q, wantWarned %v", stderr.String(), tt.wantWarned)
			}
		})
	}
}

// validKnobDocument builds the minimal input document mainRun needs to get
// past every required knob (DAEMON_APP, BASE_BRANCH, MAX_PARALLEL) and reach
// the DAEMON_AWAKE_WINDOW parse step, so awake-window tests below fail (or
// don't) for the reason they're actually testing rather than an earlier
// missing-knob error.
func validKnobDocument() *inputDocument {
	return &inputDocument{Settings: map[string]string{
		"DAEMON_APP":   ".#dogfood",
		"BASE_BRANCH":  "main",
		"MAX_PARALLEL": "1",
	}}
}

// writeInputDocument writes doc as JSON to a temp file and returns its path,
// the shape mainRun's --input flag expects.
func writeInputDocument(t *testing.T, doc *inputDocument) string {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}
	return path
}

// TestMainRun_BadAwakeWindow pins that a malformed DAEMON_AWAKE_WINDOW from
// the environment is rejected at startup: exit 1, with stderr naming the
// knob and quoting the bad value, for both a malformed span and a bad zone
// name.
func TestMainRun_BadAwakeWindow(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "malformed span", raw: "bad-window"},
		{name: "bad zone", raw: "22:00-06:00 Not/AZone"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DAEMON_AWAKE_WINDOW", tt.raw)
			path := writeInputDocument(t, validKnobDocument())
			var stdout, stderr bytes.Buffer
			got := mainRun([]string{"--input", path}, &stdout, &stderr)
			if got != 1 {
				t.Errorf("mainRun() = %d, want 1", got)
			}
			if !strings.Contains(stderr.String(), "DAEMON_AWAKE_WINDOW") {
				t.Errorf("stderr = %q, want it to name DAEMON_AWAKE_WINDOW", stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.raw) {
				t.Errorf("stderr = %q, want it to quote %q", stderr.String(), tt.raw)
			}
		})
	}
}

// TestMainRun_AbsentAwakeWindowIsNotAnError asserts an absent
// DAEMON_AWAKE_WINDOW proceeds exactly as startup does today: no
// "no value for DAEMON_AWAKE_WINDOW" diagnostic, and mainRun fails for the
// same reason it already does without this knob at all (no --input, so
// parseArgs fails first) rather than for a DAEMON_AWAKE_WINDOW complaint.
func TestMainRun_AbsentAwakeWindowIsNotAnError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"dispatch"}, &stdout, &stderr)
	if got != 1 {
		t.Errorf("mainRun() = %d, want 1", got)
	}
	if strings.Contains(stderr.String(), "DAEMON_AWAKE_WINDOW") {
		t.Errorf("stderr = %q, want no mention of DAEMON_AWAKE_WINDOW", stderr.String())
	}
}

// TestMainRun_ValidAwakeWindowReachesRepoRoot asserts a valid
// DAEMON_AWAKE_WINDOW resolved from the document's settings parses cleanly
// and mainRun proceeds past knob resolution. There is no seam onto
// daemon.Config from here, so this uses repoRoot's own fast-failing check as
// an observable proxy: chdir into a non-git directory and confirm the
// failure is repoRoot's "not a git checkout", not a DAEMON_AWAKE_WINDOW
// diagnostic — proof the window resolved and parsed before mainRun moved on.
func TestMainRun_ValidAwakeWindowReachesRepoRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	doc := validKnobDocument()
	doc.Settings["DAEMON_AWAKE_WINDOW"] = "22:00-06:00 Europe/London"
	// A bare invocation draws from both kinds, which makes
	// RESEARCH_RESERVATION a required knob: without it startup fails there,
	// short of the repoRoot check this test reads as its proxy.
	doc.Settings["RESEARCH_RESERVATION"] = "0"
	path := writeInputDocument(t, doc)

	dir := t.TempDir()
	t.Chdir(dir)

	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"--input", path}, &stdout, &stderr)
	if got != 1 {
		t.Errorf("mainRun() = %d, want 1", got)
	}
	if strings.Contains(stderr.String(), "DAEMON_AWAKE_WINDOW") {
		t.Errorf("stderr = %q, want no DAEMON_AWAKE_WINDOW diagnostic", stderr.String())
	}
	if !strings.Contains(stderr.String(), "not a git checkout") {
		t.Errorf("stderr = %q, want repoRoot's not-a-git-checkout error", stderr.String())
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
		name      string
		args      []string
		wantPath  string
		wantKinds []daemon.Kind
		wantErr   bool
	}{
		{
			// No positional verb draws from both kinds off one pool (issue
			// #3541) — the headline behaviour change of this slice.
			name:      "no positional verb defaults to both kinds",
			args:      []string{"--input", "/tmp/in.json"},
			wantPath:  "/tmp/in.json",
			wantKinds: []daemon.Kind{daemon.KindDispatch, daemon.KindResearch},
		},
		{
			name:      "explicit dispatch is work-only",
			args:      []string{"--input", "/tmp/in.json", "dispatch"},
			wantPath:  "/tmp/in.json",
			wantKinds: []daemon.Kind{daemon.KindDispatch},
		},
		{
			name:      "explicit research",
			args:      []string{"--input", "/tmp/in.json", "research"},
			wantPath:  "/tmp/in.json",
			wantKinds: []daemon.Kind{daemon.KindResearch},
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
			if got.InputPath != tt.wantPath || !slices.Equal(got.Kinds, tt.wantKinds) {
				t.Errorf("parseArgs(%v) = %+v, want {InputPath:%q Kinds:%v}", tt.args, got, tt.wantPath, tt.wantKinds)
			}
		})
	}
}

// TestParseResearchReservation pins RESEARCH_RESERVATION's parse step:
// lib/env-schema.nix declares it intKind = "nonneg" and bounds it at
// MAX_PARALLEL, so anything else — unparsable, negative, or over the slot
// count — must fail startup with a diagnostic naming both numbers, rather
// than reach daemon.Loop's own out-of-range halt.
func TestParseResearchReservation(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		slots      int
		want       int
		wantErr    bool
		wantErrHas []string
	}{
		{name: "zero is valid", raw: "0", slots: 3, want: 0},
		{name: "equal to slots is valid", raw: "3", slots: 3, want: 3},
		{name: "between zero and slots is valid", raw: "1", slots: 3, want: 1},
		{name: "non-numeric rejected", raw: "many", slots: 3, wantErr: true},
		{name: "negative rejected", raw: "-1", slots: 3, wantErr: true},
		{
			name:       "greater than slots rejected, names both values",
			raw:        "5",
			slots:      3,
			wantErr:    true,
			wantErrHas: []string{"5", "3"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseResearchReservation(tt.raw, tt.slots)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseResearchReservation(%q, %d) = %d, nil; want an error", tt.raw, tt.slots, got)
				}
				for _, want := range tt.wantErrHas {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("parseResearchReservation(%q, %d) error = %q, want it to contain %q", tt.raw, tt.slots, err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("parseResearchReservation(%q, %d) unexpected error: %v", tt.raw, tt.slots, err)
			}
			if got != tt.want {
				t.Errorf("parseResearchReservation(%q, %d) = %d, want %d", tt.raw, tt.slots, got, tt.want)
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

func TestNixSystemDouble(t *testing.T) {
	tests := []struct {
		goos    string
		goarch  string
		want    string
		wantErr bool
	}{
		{goos: "linux", goarch: "amd64", want: "x86_64-linux"},
		{goos: "linux", goarch: "arm64", want: "aarch64-linux"},
		{goos: "darwin", goarch: "arm64", want: "aarch64-darwin"},
		{goos: "windows", goarch: "amd64", wantErr: true},
		{goos: "linux", goarch: "riscv64", wantErr: true},
	}
	for _, tt := range tests {
		got, err := nixSystemDouble(tt.goos, tt.goarch)
		if tt.wantErr {
			if err == nil {
				t.Errorf("nixSystemDouble(%q, %q) = %q, want error", tt.goos, tt.goarch, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("nixSystemDouble(%q, %q) unexpected error: %v", tt.goos, tt.goarch, err)
			continue
		}
		if got != tt.want {
			t.Errorf("nixSystemDouble(%q, %q) = %q, want %q", tt.goos, tt.goarch, got, tt.want)
		}
	}
}

// TestExitCodeFor pins the daemon process's exit-code contract: 0 for an
// operator stop, exitSelfChanged (10) for — and only for — a self-changed
// halt, 1 for everything else (issue #3543).
func TestExitCodeFor(t *testing.T) {
	tests := []struct {
		reason string
		want   int
	}{
		{"context-cancelled: context canceled", 0},
		{"outcome: signalled-stop", 0},
		{"self-changed: /nix/store/old-daemon != /nix/store/new-daemon", exitSelfChanged},
		{"outcome: host-tainted", 1},
		{"outcome: config-invalid", 1},
		{"resolve-revision: boom", 1},
		{"run-child: boom", 1},
	}
	for _, tt := range tests {
		if got := exitCodeFor(tt.reason); got != tt.want {
			t.Errorf("exitCodeFor(%q) = %d, want %d", tt.reason, got, tt.want)
		}
	}
}

// TestHandleStopSignals asserts the first signal cancels ctx and forwards
// once, the second forwards again (the escalation) without cancelling twice
// or panicking, and a third is a no-op — the daemon's signal-wiring
// goroutine, extracted out of main so the halt path is exercised without
// sending the test process a real signal (issue #3538, escalation #3546).
// Table-driven over signal-kind combinations because only first-vs-second
// matters, never which kind (mirrors cmd/launcher/main.go's relaySignals
// contract).
func TestHandleStopSignals(t *testing.T) {
	tests := []struct {
		name   string
		first  os.Signal
		second os.Signal
	}{
		{"term-term", syscall.SIGTERM, syscall.SIGTERM},
		{"term-int", syscall.SIGTERM, syscall.SIGINT},
		{"int-int", syscall.SIGINT, syscall.SIGINT},
		{"int-term", syscall.SIGINT, syscall.SIGTERM},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sig := make(chan os.Signal, 2)
			quit := make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())

			var mu sync.Mutex
			forwardCalls := 0
			forward := func() {
				mu.Lock()
				forwardCalls++
				mu.Unlock()
			}

			var buf bytes.Buffer
			em := daemon.NewEmitter(&buf, time.Now)

			done := make(chan struct{})
			go func() {
				handleStopSignals(sig, quit, cancel, forward, em)
				close(done)
			}()

			sig <- tt.first
			sig <- tt.second

			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("handleStopSignals did not return after two signals")
			}

			if ctx.Err() == nil {
				t.Error("ctx.Err() = nil, want context cancelled")
			}
			mu.Lock()
			got := forwardCalls
			mu.Unlock()
			if got != 2 {
				t.Errorf("forward called %d times, want 2", got)
			}

			// A third signal, sent after the handler has already
			// returned (done is closed above), must never bump
			// forwardCalls: the handler drains sig only twice and
			// nothing else reads it, so there is no race to wait out.
			sig <- syscall.SIGTERM
			mu.Lock()
			got = forwardCalls
			mu.Unlock()
			if got != 2 {
				t.Errorf("forward called %d times after a third signal, want 2 (third must be a no-op)", got)
			}

			lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
			if len(lines) != 2 {
				t.Fatalf("event stream has %d lines, want 2: %q", len(lines), buf.String())
			}
			var drain, escalate daemon.Event
			if err := json.Unmarshal([]byte(lines[0]), &drain); err != nil {
				t.Fatalf("decode drain event %q: %v", lines[0], err)
			}
			if err := json.Unmarshal([]byte(lines[1]), &escalate); err != nil {
				t.Fatalf("decode escalate event %q: %v", lines[1], err)
			}
			if drain.Event != "shutdown" || drain.Reason != daemon.ShutdownDrain {
				t.Errorf("first event = %+v, want event=shutdown reason=%q", drain, daemon.ShutdownDrain)
			}
			if escalate.Event != "shutdown" || escalate.Reason != daemon.ShutdownEscalate {
				t.Errorf("second event = %+v, want event=shutdown reason=%q", escalate, daemon.ShutdownEscalate)
			}
		})
	}
}

// TestHandleStopSignals_QuitBeforeFirstSignal asserts that closing quit
// before any signal arrives unparks handleStopSignals rather than leaving it
// blocked on <-sig forever — the leak mainRun's repeated test-driving would
// otherwise accumulate one goroutine per call (issue #3546).
func TestHandleStopSignals_QuitBeforeFirstSignal(t *testing.T) {
	sig := make(chan os.Signal, 2)
	quit := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	forwardCalls := 0
	forward := func() {
		mu.Lock()
		forwardCalls++
		mu.Unlock()
	}

	var buf bytes.Buffer
	em := daemon.NewEmitter(&buf, time.Now)

	done := make(chan struct{})
	go func() {
		handleStopSignals(sig, quit, cancel, forward, em)
		close(done)
	}()

	close(quit)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleStopSignals did not return after quit closed before any signal")
	}

	if ctx.Err() != nil {
		t.Errorf("ctx.Err() = %v, want nil: quit before a signal must not cancel", ctx.Err())
	}
	mu.Lock()
	got := forwardCalls
	mu.Unlock()
	if got != 0 {
		t.Errorf("forward called %d times, want 0", got)
	}
	if buf.Len() != 0 {
		t.Errorf("event stream = %q, want empty", buf.String())
	}
}

// TestHandleStopSignals_QuitAfterFirstSignal asserts that closing quit after
// the first signal but before the second still returns handleStopSignals,
// with the first signal's cancel/forward/shutdown-event side effects already
// applied — the second select's quit case must unpark it just as the
// first's does.
func TestHandleStopSignals_QuitAfterFirstSignal(t *testing.T) {
	sig := make(chan os.Signal, 2)
	quit := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())

	var mu sync.Mutex
	forwardCalls := 0
	forward := func() {
		mu.Lock()
		forwardCalls++
		mu.Unlock()
	}

	var buf bytes.Buffer
	em := daemon.NewEmitter(&buf, time.Now)

	done := make(chan struct{})
	go func() {
		handleStopSignals(sig, quit, cancel, forward, em)
		close(done)
	}()

	sig <- syscall.SIGTERM

	// Give the goroutine a chance to consume the first signal and apply its
	// side effects before quit closes, so this test observes "quit while
	// waiting on the second" rather than racing the first receive itself.
	for i := 0; i < 1000; i++ {
		mu.Lock()
		forwarded := forwardCalls != 0
		mu.Unlock()
		if forwarded {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(quit)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleStopSignals did not return after quit closed between signals")
	}

	if ctx.Err() == nil {
		t.Error("ctx.Err() = nil, want context cancelled from the first signal")
	}
	mu.Lock()
	got := forwardCalls
	mu.Unlock()
	if got != 1 {
		t.Errorf("forward called %d times, want 1", got)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("event stream has %d lines, want 1: %q", len(lines), buf.String())
	}
	var drain daemon.Event
	if err := json.Unmarshal([]byte(lines[0]), &drain); err != nil {
		t.Fatalf("decode drain event %q: %v", lines[0], err)
	}
	if drain.Event != "shutdown" || drain.Reason != daemon.ShutdownDrain {
		t.Errorf("event = %+v, want event=shutdown reason=%q", drain, daemon.ShutdownDrain)
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

// writeInputDocT writes an inputDocument with the given settings to a fresh
// file in t.TempDir() and returns its path.
func writeInputDocT(t *testing.T, settings map[string]string) string {
	t.Helper()
	data, err := json.Marshal(inputDocument{Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "in.json")
	writeFileT(t, path, string(data))
	return path
}

// clearKnobEnvT clears the daemon's knob env vars for the duration of the
// test, so an ambient export in the host/CI environment cannot shadow the
// input document values these tests set up.
func clearKnobEnvT(t *testing.T) {
	t.Helper()
	for _, v := range []string{"DAEMON_APP", "BASE_BRANCH", "MAX_PARALLEL", "RESEARCH_RESERVATION"} {
		t.Setenv(v, "")
	}
}

// TestMainRun_WorkOnlyIgnoresExcessiveReservation asserts a `dispatch`
// invocation is never failed by RESEARCH_RESERVATION, however it is set:
// the knob is inert for a single-kind daemon, so mainRun must not even
// resolve or validate it there. The input document's MAX_PARALLEL=1 with
// RESEARCH_RESERVATION=5 would fail parseResearchReservation outright if it
// ran; running cwd from a non-git tempdir instead surfaces repoRoot's "not
// a git checkout" failure, proving startup got past knob resolution.
func TestMainRun_WorkOnlyIgnoresExcessiveReservation(t *testing.T) {
	clearKnobEnvT(t)
	docPath := writeInputDocT(t, map[string]string{
		"DAEMON_APP":           ".#dogfood",
		"BASE_BRANCH":          "main",
		"MAX_PARALLEL":         "1",
		"RESEARCH_RESERVATION": "5",
	})
	t.Chdir(t.TempDir())

	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"--input", docPath, "dispatch"}, &stdout, &stderr)
	if got != 1 {
		t.Fatalf("mainRun() = %d, want 1", got)
	}
	if strings.Contains(stderr.String(), "RESEARCH_RESERVATION") {
		t.Errorf("stderr = %q, RESEARCH_RESERVATION must be inert for a work-only daemon", stderr.String())
	}
	if !strings.Contains(stderr.String(), "not a git checkout") {
		t.Errorf("stderr = %q, want the repoRoot failure (proves knob resolution completed)", stderr.String())
	}
}

// TestMainRun_BothKindsValidatesReservation is the both-kinds counterpart:
// with no positional verb the daemon draws from both kinds (parseArgs'
// default), so the same RESEARCH_RESERVATION=5/MAX_PARALLEL=1 document must
// now fail startup at the reservation check, before ever reaching
// repoRoot — this is the seam the brief's "input document's
// RESEARCH_RESERVATION reaches daemon.Config" requirement is covered at,
// since mainRun has no seam to observe the daemon.Config value itself.
func TestMainRun_BothKindsValidatesReservation(t *testing.T) {
	clearKnobEnvT(t)
	docPath := writeInputDocT(t, map[string]string{
		"DAEMON_APP":           ".#dogfood",
		"BASE_BRANCH":          "main",
		"MAX_PARALLEL":         "1",
		"RESEARCH_RESERVATION": "5",
	})
	t.Chdir(t.TempDir())

	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"--input", docPath}, &stdout, &stderr)
	if got != 1 {
		t.Fatalf("mainRun() = %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "RESEARCH_RESERVATION") {
		t.Errorf("stderr = %q, want it to mention RESEARCH_RESERVATION", stderr.String())
	}
	if strings.Contains(stderr.String(), "not a git checkout") {
		t.Errorf("stderr = %q, want startup to fail at the reservation check, before repoRoot", stderr.String())
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

// TestGitDir_ResolvesAbsoluteGitDir asserts gitDir run from a subdirectory
// of a checkout resolves to the checkout's own .git dir, not the working
// tree root — the checkout lock (issue #3543) must land there so it never
// touches the operator's working tree.
func TestGitDir_ResolvesAbsoluteGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	gitRunT(t, root, "-c", "init.defaultBranch=main", "init")
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := gitDir(sub)
	if err != nil {
		t.Fatalf("gitDir() unexpected error: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("gitDir() = %q, want an absolute path", got)
	}
	if !strings.HasSuffix(got, ".git") {
		t.Errorf("gitDir() = %q, want a path ending in .git", got)
	}
}

// TestGitDir_NotAGitCheckout mirrors TestRepoRoot_NotAGitCheckout: gitDir
// must fail fast, naming the offending dir, outside any checkout.
func TestGitDir_NotAGitCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	_, err := gitDir(dir)
	if err == nil {
		t.Fatal("gitDir() error = nil, want an error for a non-git directory")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("gitDir() error = %q, want it to name %q", err, dir)
	}
}

// TestMainRun_InstanceLockRefusal is the acceptance-criterion test: a
// second daemon against a checkout whose lock is already held must refuse
// before ever reaching daemon.Loop (no child, no network, no nix — fully
// deterministic), reporting the refusal both on stderr (naming the holder)
// and as a "halt" event on stdout's durable JSON-lines stream (issue #3543).
func TestMainRun_InstanceLockRefusal(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	gitRunT(t, root, "-c", "init.defaultBranch=main", "init")

	gitDirPath, err := gitDir(root)
	if err != nil {
		t.Fatalf("gitDir(%q): %v", root, err)
	}

	// Hold the lock ourselves, in-process, standing in for "another daemon
	// already running against this checkout".
	lock, err := daemon.AcquireCheckoutLock(gitDirPath, []daemon.Kind{daemon.KindDispatch})
	if err != nil {
		t.Fatalf("AcquireCheckoutLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	inputPath := filepath.Join(t.TempDir(), "input.json")
	// A bare invocation draws from both kinds, which makes
	// RESEARCH_RESERVATION a required knob: without it startup fails there,
	// short of the lock acquire this test exists to exercise.
	doc := inputDocument{Settings: map[string]string{
		"DAEMON_APP":           ".#dogfood",
		"BASE_BRANCH":          "main",
		"MAX_PARALLEL":         "1",
		"RESEARCH_RESERVATION": "0",
	}}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal input document: %v", err)
	}
	if err := os.WriteFile(inputPath, data, 0o644); err != nil {
		t.Fatalf("write input document: %v", err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore Chdir: %v", err)
		}
	})
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir(%q): %v", root, err)
	}

	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"--input", inputPath}, &stdout, &stderr)
	if got != 1 {
		t.Errorf("mainRun() = %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "pid=") {
		t.Errorf("stderr = %q, want it to name the lock holder (pid=...)", stderr.String())
	}
	if !strings.Contains(stderr.String(), strconv.Itoa(os.Getpid())) {
		t.Errorf("stderr = %q, want it to contain the holding pid %d", stderr.String(), os.Getpid())
	}

	line := strings.TrimSpace(stdout.String())
	if line == "" {
		t.Fatal("stdout is empty, want a halt event line")
	}
	var ev daemon.Event
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("decode stdout halt event %q: %v", line, err)
	}
	if ev.Event != "halt" {
		t.Errorf("event = %q, want %q", ev.Event, "halt")
	}
	if !strings.HasPrefix(ev.Reason, "instance-lock:") {
		t.Errorf("reason = %q, want it to start with %q", ev.Reason, "instance-lock:")
	}
}

// TestBreakerDefaults_TripReachableAtOneSlot pins the reachability the
// breaker exists for at the smallest supported pool: at MAX_PARALLEL=1 a
// systemic fault's failures are one slot's own retries, spaced
// daemonFailureBackoff apart, so daemonBreakerThreshold of them must still
// fit inside daemonBreakerWindow. Values that push that span past the
// window leave a 1-slot daemon burning its only slot until morning with no
// breaker_trip ever emitted.
func TestBreakerDefaults_TripReachableAtOneSlot(t *testing.T) {
	span := time.Duration(daemonBreakerThreshold-1) * daemonFailureBackoff
	if span >= daemonBreakerWindow {
		t.Fatalf("a single slot can never trip the breaker: %d failures at %s apart span %s, outside the %s window",
			daemonBreakerThreshold, daemonFailureBackoff, span, daemonBreakerWindow)
	}
}

// TestMainRun_StatusDoesNotRequireInput is the regression the pre-parseArgs
// dispatch in mainRun exists to prevent: `daemon status` with no --input
// must never hit parseArgs's "flag --input is required" error, since
// reading status is not running a daemon.
func TestMainRun_StatusDoesNotRequireInput(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	gitRunT(t, root, "-c", "init.defaultBranch=main", "init")
	t.Chdir(root)

	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"status"}, &stdout, &stderr)
	if got != 0 {
		t.Fatalf("mainRun() = %d, want 0; stderr=%q", got, stderr.String())
	}
	if strings.Contains(stderr.String(), "--input") {
		t.Errorf("stderr = %q, want no mention of --input", stderr.String())
	}
}

// TestCmdStatus_NeverRan covers the no-daemon-ever-ran case: exit 0, a
// StatusReport that unmarshals with Live false and no Status, and a stderr
// sentence saying so.
func TestCmdStatus_NeverRan(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	gitRunT(t, root, "-c", "init.defaultBranch=main", "init")

	var stdout, stderr bytes.Buffer
	got := cmdStatus(root, &stdout, &stderr)
	if got != 0 {
		t.Fatalf("cmdStatus() = %d, want 0; stderr=%q", got, stderr.String())
	}
	var report daemon.StatusReport
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &report); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	if report.Live {
		t.Errorf("report.Live = true, want false")
	}
	if report.Status != nil {
		t.Errorf("report.Status = %+v, want nil", report.Status)
	}
	if !strings.Contains(stderr.String(), "no daemon has run") {
		t.Errorf("stderr = %q, want it to say no daemon has run", stderr.String())
	}
}

// TestCmdStatus_Live covers the happy path: a lock held in-process (standing
// in for a running daemon) plus a status file published by a StatusWriter
// in the same process, so the pids match and ReadStatus reports Live.
func TestCmdStatus_Live(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	gitRunT(t, root, "-c", "init.defaultBranch=main", "init")
	gitDirPath, err := gitDir(root)
	if err != nil {
		t.Fatalf("gitDir(%q): %v", root, err)
	}

	lock, err := daemon.AcquireCheckoutLock(gitDirPath, []daemon.Kind{daemon.KindDispatch})
	if err != nil {
		t.Fatalf("AcquireCheckoutLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	sw := daemon.NewStatusWriter(gitDirPath, time.Now)
	if err := sw.Write(daemon.Status{
		State: daemon.StateWorking,
		Slots: []daemon.SlotStatus{
			{Slot: 0, Busy: true, Kind: daemon.KindDispatch, Issues: []string{"101"}},
		},
	}); err != nil {
		t.Fatalf("StatusWriter.Write: %v", err)
	}

	var stdout, stderr bytes.Buffer
	got := cmdStatus(root, &stdout, &stderr)
	if got != 0 {
		t.Fatalf("cmdStatus() = %d, want 0; stderr=%q", got, stderr.String())
	}
	var report daemon.StatusReport
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &report); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	if !report.Live {
		t.Errorf("report.Live = false, want true")
	}
	if !strings.Contains(stderr.String(), string(daemon.StateWorking)) {
		t.Errorf("stderr = %q, want it to name the state %q", stderr.String(), daemon.StateWorking)
	}
}

// TestCmdStatus_Stale covers a status file whose pid names a different
// process while the lock is unheld: ReadStatus must report Stale, never
// Live, and cmdStatus's stderr must say so plainly.
func TestCmdStatus_Stale(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	gitRunT(t, root, "-c", "init.defaultBranch=main", "init")
	gitDirPath, err := gitDir(root)
	if err != nil {
		t.Fatalf("gitDir(%q): %v", root, err)
	}

	sw := daemon.NewStatusWriter(gitDirPath, time.Now)
	if err := sw.Write(daemon.Status{State: daemon.StateHalted, Reason: "operator stop"}); err != nil {
		t.Fatalf("StatusWriter.Write: %v", err)
	}
	// No lock held: NewStatusWriter does not acquire one, and this test
	// never calls AcquireCheckoutLock — the published Pid is this test
	// process's own pid, but with the lock unheld ReadStatus's liveness
	// probe fails regardless of whose pid is in the file.

	var stdout, stderr bytes.Buffer
	got := cmdStatus(root, &stdout, &stderr)
	if got != 0 {
		t.Fatalf("cmdStatus() = %d, want 0; stderr=%q", got, stderr.String())
	}
	var report daemon.StatusReport
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &report); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	if report.Live {
		t.Errorf("report.Live = true, want false")
	}
	if !report.Stale {
		t.Errorf("report.Stale = false, want true")
	}
	if !strings.Contains(stderr.String(), "stale") {
		t.Errorf("stderr = %q, want it to say stale", stderr.String())
	}
}

// TestCmdStatus_GarbageStatusFileStillReportsLiveness pins the review
// finding at cmdStatus: a genuinely held lock beside a garbage status file
// must not suppress the liveness truth just because the advisory file
// failed to parse. cmdStatus must still exit 1 (the ReadStatus error is
// real), but must print summarizeStatus's liveness sentence to stderr
// before the failure line.
func TestCmdStatus_GarbageStatusFileStillReportsLiveness(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	gitRunT(t, root, "-c", "init.defaultBranch=main", "init")
	gitDirPath, err := gitDir(root)
	if err != nil {
		t.Fatalf("gitDir(%q): %v", root, err)
	}

	lock, err := daemon.AcquireCheckoutLock(gitDirPath, []daemon.Kind{daemon.KindDispatch})
	if err != nil {
		t.Fatalf("AcquireCheckoutLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	statusPath := filepath.Join(gitDirPath, "spindrift-daemon.status")
	if err := os.WriteFile(statusPath, []byte("not json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var stdout, stderr bytes.Buffer
	got := cmdStatus(root, &stdout, &stderr)
	if got != 1 {
		t.Fatalf("cmdStatus() = %d, want 1; stderr=%q", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "holds this checkout") {
		t.Errorf("stderr = %q, want it to report the held lock despite the parse error", stderr.String())
	}
}

// TestSummarizeStatus_LockHeldDistinguishesNoStatusFromUncorrelated pins the
// review finding that the LockHeld branch used one wording ("has not
// published its status yet") even when a status file was present but named
// a predecessor, not the current holder — the two cases must read
// differently.
func TestSummarizeStatus_LockHeldDistinguishesNoStatusFromUncorrelated(t *testing.T) {
	noStatus := daemon.StatusReport{LockHeld: true, Holder: "pid=1 host=h kind=dispatch started=x"}
	got := summarizeStatus(noStatus)
	if !strings.Contains(got, "has not published its status yet") {
		t.Errorf("summarizeStatus(no status) = %q, want it to say the holder has not published yet", got)
	}

	uncorrelated := daemon.StatusReport{
		LockHeld: true,
		Holder:   "pid=1 host=h kind=dispatch started=x",
		Status:   &daemon.Status{Pid: 999, State: daemon.StateWorking},
	}
	got = summarizeStatus(uncorrelated)
	if strings.Contains(got, "has not published its status yet") {
		t.Errorf("summarizeStatus(uncorrelated status) = %q, want it to distinguish a predecessor's leftover status", got)
	}
	if !strings.Contains(got, "999") {
		t.Errorf("summarizeStatus(uncorrelated status) = %q, want it to name the leftover status's own pid", got)
	}
}

// TestMainRun_StatusExtraArgument asserts an extra positional argument
// after "status" is a usage error, not silently ignored.
func TestMainRun_StatusExtraArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"status", "dispatch"}, &stdout, &stderr)
	if got != 1 {
		t.Errorf("mainRun() = %d, want 1", got)
	}
	if stderr.String() == "" {
		t.Error("stderr is empty, want a usage error")
	}
}

// TestMainRun_StatusOutsideGitCheckout asserts `daemon status` fails with
// gitDir's own error, exit 1, outside any checkout.
func TestMainRun_StatusOutsideGitCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Chdir(t.TempDir())

	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"status"}, &stdout, &stderr)
	if got != 1 {
		t.Fatalf("mainRun() = %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "not a git checkout") {
		t.Errorf("stderr = %q, want it to name the gitDir failure", stderr.String())
	}
}

// TestExitSelfChanged_OutsideChildExitBand pins the invariant exitSelfChanged's
// own doc comment claims but nothing enforces: it must fall outside the 0-7
// band a *child* launcher's exit codes occupy (daemon.Interpret), so an
// operator restarting a service unit on exitSelfChanged alone can never be
// triggered by a child's exit leaking through. Fails if exitSelfChanged is
// ever lowered into that band.
func TestExitSelfChanged_OutsideChildExitBand(t *testing.T) {
	for exit := 0; exit < 256; exit++ {
		outcome, action := daemon.Interpret(exit)
		// The band is two things, and neither alone is all of it: the 0-7 span
		// the doc names (exit 1 is a child's generic failure, which Interpret
		// leaves unclassified), plus every code Interpret *does* classify, so a
		// `case 10:` added there later collides here instead of silently.
		if exit > 7 && action == daemon.Backoff {
			continue
		}
		if exitSelfChanged == exit {
			t.Fatalf("exitSelfChanged (%d) collides with child exit code %d, which daemon.Interpret classifies as %q",
				exitSelfChanged, exit, outcome)
		}
	}
}

// fakePreflightRunner is startupPreflight's test seam: a minimal
// preflightRunner that never shells out, scripted per test.
type fakePreflightRunner struct {
	revision   string
	resolveErr error
	doctorExit int
	doctorErr  error

	mu          sync.Mutex
	doctorCalls int
}

func (f *fakePreflightRunner) ResolveRevision(ctx context.Context) (string, error) {
	if f.resolveErr != nil {
		return "", f.resolveErr
	}
	return f.revision, nil
}

func (f *fakePreflightRunner) RunDoctor(ctx context.Context, revision string) (int, error) {
	f.mu.Lock()
	f.doctorCalls++
	f.mu.Unlock()
	if f.doctorErr != nil {
		return 0, f.doctorErr
	}
	return f.doctorExit, nil
}

func (f *fakePreflightRunner) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.doctorCalls
}

// TestPublishHaltedStatus pins what mainRun's preflight-refusal branch relies
// on: with no pool yet to snapshot through, a direct Write still lands a
// StateHalted status naming the same reason and kinds the caller passed.
func TestPublishHaltedStatus(t *testing.T) {
	dir := t.TempDir()
	sw := daemon.NewStatusWriter(dir, func() time.Time { return time.Unix(0, 0).UTC() })
	var stderr bytes.Buffer

	publishHaltedStatus(&stderr, sw, []daemon.Kind{daemon.KindDispatch}, "preflight: doctor-config-invalid")

	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty on a successful write", stderr.String())
	}
	report, err := daemon.ReadStatus(dir)
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	if report.Status == nil {
		t.Fatal("status file missing after publishHaltedStatus")
	}
	if report.Status.State != daemon.StateHalted {
		t.Errorf("state = %q, want %q", report.Status.State, daemon.StateHalted)
	}
	if report.Status.Reason != "preflight: doctor-config-invalid" {
		t.Errorf("reason = %q, want %q", report.Status.Reason, "preflight: doctor-config-invalid")
	}
	if len(report.Status.Kinds) != 1 || report.Status.Kinds[0] != daemon.KindDispatch {
		t.Errorf("kinds = %v, want [dispatch]", report.Status.Kinds)
	}
}

// decodePreflightEvents parses buf's JSON-lines stream, one daemon.Event per
// non-empty line, in emitted order.
func decodePreflightEvents(t *testing.T, buf *bytes.Buffer) []daemon.Event {
	t.Helper()
	var events []daemon.Event
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var ev daemon.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("decode event line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

// TestStartupPreflight_Healthy is the "does not conflate looking healthy
// with doing nothing" positive case: doctor exit 0 lets the daemon proceed
// and stamps exactly one "preflight" event, no "halt" (issue #3544).
func TestStartupPreflight_Healthy(t *testing.T) {
	var buf bytes.Buffer
	em := daemon.NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
	r := &fakePreflightRunner{revision: "deadbeef", doctorExit: 0}

	reason := startupPreflight(context.Background(), r, em)
	if reason != "" {
		t.Fatalf("startupPreflight() = %q, want \"\"", reason)
	}
	if r.calls() != 1 {
		t.Fatalf("RunDoctor called %d times, want 1", r.calls())
	}

	events := decodePreflightEvents(t, &buf)
	if len(events) != 1 {
		t.Fatalf("events = %+v, want exactly one", events)
	}
	ev := events[0]
	if ev.Event != "preflight" {
		t.Errorf("event = %q, want %q", ev.Event, "preflight")
	}
	if ev.Outcome != "doctor-healthy" {
		t.Errorf("outcome = %q, want %q", ev.Outcome, "doctor-healthy")
	}
	if ev.Exit == nil || *ev.Exit != 0 {
		t.Errorf("exit = %v, want pointer to 0", ev.Exit)
	}
	if ev.Revision != "deadbeef" {
		t.Errorf("revision = %q, want %q", ev.Revision, "deadbeef")
	}
}

// TestStartupPreflight_RequiredLabelsMissing pins the acceptance criterion
// that the failure names what failed and its remedy, on doctor's own exit 4
// (missing required triage labels).
func TestStartupPreflight_RequiredLabelsMissing(t *testing.T) {
	var buf bytes.Buffer
	em := daemon.NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
	r := &fakePreflightRunner{revision: "deadbeef", doctorExit: 4}

	reason := startupPreflight(context.Background(), r, em)
	if !strings.HasPrefix(reason, daemon.HaltPreflightPrefix) {
		t.Fatalf("reason = %q, want prefix %q", reason, daemon.HaltPreflightPrefix)
	}
	if !strings.Contains(reason, "required triage labels are missing") {
		t.Errorf("reason = %q, want it to name what failed", reason)
	}
	if !strings.Contains(reason, "create the four triage labels") {
		t.Errorf("reason = %q, want it to carry the remedy", reason)
	}

	events := decodePreflightEvents(t, &buf)
	if len(events) != 2 {
		t.Fatalf("events = %+v, want a preflight event and a halt event", events)
	}
	if events[0].Event != "preflight" || events[0].Outcome != "doctor-required-labels-missing" {
		t.Errorf("events[0] = %+v, want preflight/doctor-required-labels-missing", events[0])
	}
	if events[1].Event != "halt" || events[1].Reason != reason {
		t.Errorf("events[1] = %+v, want halt with reason %q", events[1], reason)
	}
}

// TestStartupPreflight_ConfigInvalid covers doctor exit 2 — the exit an
// undersized podman machine now arrives as (slice 2) — refusing to start.
func TestStartupPreflight_ConfigInvalid(t *testing.T) {
	var buf bytes.Buffer
	em := daemon.NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
	r := &fakePreflightRunner{revision: "deadbeef", doctorExit: 2}

	reason := startupPreflight(context.Background(), r, em)
	if reason == "" {
		t.Fatal("startupPreflight() = \"\", want a refusal for doctor exit 2")
	}
}

// TestStartupPreflight_Connectivity covers doctor exit 3 refusing to start.
func TestStartupPreflight_Connectivity(t *testing.T) {
	var buf bytes.Buffer
	em := daemon.NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
	r := &fakePreflightRunner{revision: "deadbeef", doctorExit: 3}

	reason := startupPreflight(context.Background(), r, em)
	if reason == "" {
		t.Fatal("startupPreflight() = \"\", want a refusal for doctor exit 3")
	}
}

// TestStartupPreflight_RunsExactlyOnce asserts RunDoctor is called exactly
// once across a whole startupPreflight call. The stronger structural claim —
// "called once per daemon startup, not once per Loop iteration" — is
// established by inspection of mainRun's call site (placed before
// daemon.Loop, outside any loop), not by this test alone.
func TestStartupPreflight_RunsExactlyOnce(t *testing.T) {
	var buf bytes.Buffer
	em := daemon.NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
	r := &fakePreflightRunner{revision: "deadbeef", doctorExit: 0}

	startupPreflight(context.Background(), r, em)
	if r.calls() != 1 {
		t.Fatalf("RunDoctor called %d times, want exactly 1", r.calls())
	}
}

// TestStartupPreflight_ResolveRevisionFailure: a preflight that cannot run
// is not a preflight that passed.
func TestStartupPreflight_ResolveRevisionFailure(t *testing.T) {
	var buf bytes.Buffer
	em := daemon.NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
	r := &fakePreflightRunner{resolveErr: errors.New("git fetch boom")}

	reason := startupPreflight(context.Background(), r, em)
	if reason == "" {
		t.Fatal("startupPreflight() = \"\", want a refusal on a resolve failure")
	}
	if r.calls() != 0 {
		t.Errorf("RunDoctor called %d times, want 0 (never reached)", r.calls())
	}

	events := decodePreflightEvents(t, &buf)
	if len(events) != 2 {
		t.Fatalf("events = %+v, want a preflight error event and a halt event", events)
	}
	if events[0].Event != "preflight" || events[0].Outcome != "doctor-seam-error" {
		t.Errorf("events[0] = %+v, want preflight/doctor-seam-error", events[0])
	}
}

// TestStartupPreflight_RunDoctorSeamFailure covers RunDoctor's own seam
// error (distinct from a classified exit code) refusing to start likewise.
func TestStartupPreflight_RunDoctorSeamFailure(t *testing.T) {
	var buf bytes.Buffer
	em := daemon.NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
	r := &fakePreflightRunner{revision: "deadbeef", doctorErr: errors.New("exec boom")}

	reason := startupPreflight(context.Background(), r, em)
	if reason == "" {
		t.Fatal("startupPreflight() = \"\", want a refusal on a RunDoctor seam failure")
	}
}

// TestStartupPreflight_ContextCancelled covers both seams returning
// ctx.Err(): the resulting reason must be one isOperatorStop accepts, so a
// Ctrl-C during the preflight exits 0 rather than exitPreflightFailed.
func TestStartupPreflight_ContextCancelled(t *testing.T) {
	tests := []struct {
		name string
		r    *fakePreflightRunner
	}{
		{name: "resolve", r: &fakePreflightRunner{}},
		{name: "doctor", r: &fakePreflightRunner{revision: "deadbeef"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if tt.name == "resolve" {
				tt.r.resolveErr = ctx.Err()
			} else {
				tt.r.doctorErr = ctx.Err()
			}

			var buf bytes.Buffer
			em := daemon.NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
			reason := startupPreflight(ctx, tt.r, em)
			if !isOperatorStop(reason) {
				t.Fatalf("isOperatorStop(%q) = false, want true", reason)
			}

			// A cancelled preflight must still reach the durable event
			// stream (every other halt in this daemon does) — otherwise a
			// Ctrl-C here leaves the stream showing nothing at all between
			// process start and process exit.
			events := decodePreflightEvents(t, &buf)
			if len(events) != 2 {
				t.Fatalf("events = %+v, want a preflight event and a halt event", events)
			}
			if events[0].Event != "preflight" || events[0].Outcome != "doctor-cancelled" {
				t.Errorf("events[0] = %+v, want preflight/doctor-cancelled", events[0])
			}
			if events[1].Event != "halt" || events[1].Reason != reason {
				t.Errorf("events[1] = %+v, want halt with reason %q", events[1], reason)
			}
		})
	}
}

// TestExitPreflightFailed_DistinctFromOtherExitCodes guards exitPreflightFailed
// against ever colliding with one of this binary's other exit codes, so a
// later edit cannot make an operator-stop or a self-changed halt
// indistinguishable from a refused start.
func TestExitPreflightFailed_DistinctFromOtherExitCodes(t *testing.T) {
	for _, other := range []int{0, 1, exitSelfChanged} {
		if exitPreflightFailed == other {
			t.Fatalf("exitPreflightFailed (%d) collides with exit code %d", exitPreflightFailed, other)
		}
	}
}

// TestStartupPreflight_SignalKilledDoctorOnCancelledContext is the Ctrl-C-
// during-doctor path: the child is killed, so RunDoctor's result is no
// verdict at all (here the worst case, a bare (-1, nil) from a seam that
// failed to classify it). A cancelled ctx must still read as an operator
// stop — exit 0 — never as a preflight refusal.
func TestStartupPreflight_SignalKilledDoctorOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var buf bytes.Buffer
	em := daemon.NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
	r := &fakePreflightRunner{revision: "deadbeef", doctorExit: -1}

	reason := startupPreflight(ctx, r, em)
	if !isOperatorStop(reason) {
		t.Fatalf("startupPreflight() = %q, want an operator-stop reason", reason)
	}
	if got := exitCodeFor(reason); got != 0 {
		t.Errorf("exitCodeFor(%q) = %d, want 0", reason, got)
	}
	if strings.Contains(reason, daemon.HaltPreflightPrefix) {
		t.Errorf("startupPreflight() = %q, want no %q refusal", reason, daemon.HaltPreflightPrefix)
	}

	events := decodePreflightEvents(t, &buf)
	var sawPreflight bool
	for _, ev := range events {
		if ev.Event == "preflight" {
			sawPreflight = true
			if !strings.HasPrefix(ev.Outcome, "doctor-") {
				t.Errorf("preflight outcome = %q, want a doctor- prefixed label", ev.Outcome)
			}
			if ev.Exit != nil {
				t.Errorf("preflight exit = %d, want no exit field (no doctor verdict)", *ev.Exit)
			}
		}
	}
	if !sawPreflight {
		t.Errorf("events = %+v, want a preflight event among them", events)
	}
}
