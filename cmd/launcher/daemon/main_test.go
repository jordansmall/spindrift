package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/inputdoc"
)

// shippedKnobDefaults mirrors the five daemon tuning knobs' shipped
// lib/env-schema.nix defaults, for tests to fill an input document with.
var shippedKnobDefaults = map[string]string{
	"DAEMON_IDLE_FLOOR":        "5m",
	"DAEMON_IDLE_CAP":          "30m",
	"DAEMON_FAILURE_BACKOFF":   "1m",
	"DAEMON_BREAKER_THRESHOLD": "5",
	"DAEMON_BREAKER_WINDOW":    "15m",
}

// validKnobDocument builds the minimal input document mainRun needs to get
// past every required knob (DAEMON_APP, BASE_BRANCH, MAX_PARALLEL, and the
// five schema-defaulted backoff/breaker knobs, all set to their
// lib/env-schema.nix defaults) and reach the DAEMON_AWAKE_WINDOW parse
// step, so awake-window tests below fail (or don't) for the reason they're
// actually testing rather than an earlier missing-knob error.
func validKnobDocument() *inputdoc.Document {
	settings := map[string]string{
		"DAEMON_APP":   ".#dogfood",
		"BASE_BRANCH":  "main",
		"MAX_PARALLEL": "1",
	}
	maps.Copy(settings, shippedKnobDefaults)
	return &inputdoc.Document{Settings: settings}
}

// writeInputDocument writes doc as JSON to a temp file and returns its path,
// the shape mainRun's --input flag expects.
func writeInputDocument(t *testing.T, doc *inputdoc.Document) string {
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

// TestParseIdleFloor pins DAEMON_IDLE_FLOOR's parse step: a Go duration
// string, rejected unless strictly positive.
func TestParseIdleFloor(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{name: "shipped default", raw: "5m", want: 5 * time.Minute},
		{name: "malformed rejected", raw: "not-a-duration", wantErr: true},
		{name: "zero rejected", raw: "0s", wantErr: true},
		{name: "negative rejected", raw: "-1m", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseIdleFloor(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseIdleFloor(%q) = %v, nil; want an error", tt.raw, got)
				}
				if !strings.Contains(err.Error(), "DAEMON_IDLE_FLOOR") {
					t.Errorf("parseIdleFloor(%q) error = %q, want it to name DAEMON_IDLE_FLOOR", tt.raw, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseIdleFloor(%q) unexpected error: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("parseIdleFloor(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

// TestParseIdleCap pins DAEMON_IDLE_CAP's parse step: strictly positive,
// and never below the floor it doubles up from — the cross-knob case names
// both knobs and both values, the shape parseResearchReservation's own
// cross-knob error already uses.
func TestParseIdleCap(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		floor      time.Duration
		want       time.Duration
		wantErr    bool
		wantErrHas []string
	}{
		{name: "shipped default", raw: "30m", floor: 5 * time.Minute, want: 30 * time.Minute},
		{name: "equal to floor is valid", raw: "5m", floor: 5 * time.Minute, want: 5 * time.Minute},
		{name: "malformed rejected", raw: "not-a-duration", floor: 5 * time.Minute, wantErr: true},
		{name: "zero rejected", raw: "0s", floor: 5 * time.Minute, wantErr: true},
		{
			name:       "below floor rejected, names both knobs",
			raw:        "1m",
			floor:      5 * time.Minute,
			wantErr:    true,
			wantErrHas: []string{"DAEMON_IDLE_CAP", "DAEMON_IDLE_FLOOR", "1m0s", "5m0s"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseIdleCap(tt.raw, tt.floor)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseIdleCap(%q, %v) = %v, nil; want an error", tt.raw, tt.floor, got)
				}
				for _, want := range tt.wantErrHas {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("parseIdleCap(%q, %v) error = %q, want it to contain %q", tt.raw, tt.floor, err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("parseIdleCap(%q, %v) unexpected error: %v", tt.raw, tt.floor, err)
			}
			if got != tt.want {
				t.Errorf("parseIdleCap(%q, %v) = %v, want %v", tt.raw, tt.floor, got, tt.want)
			}
		})
	}
}

// TestParseFailureBackoff pins DAEMON_FAILURE_BACKOFF's parse step: unlike
// the idle and breaker knobs, zero is legal (retry immediately) — only a
// malformed or negative value is rejected.
func TestParseFailureBackoff(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{name: "shipped default", raw: "1m", want: time.Minute},
		{name: "zero is valid", raw: "0s", want: 0},
		{name: "malformed rejected", raw: "not-a-duration", wantErr: true},
		{name: "negative rejected", raw: "-1m", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFailureBackoff(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseFailureBackoff(%q) = %v, nil; want an error", tt.raw, got)
				}
				if !strings.Contains(err.Error(), "DAEMON_FAILURE_BACKOFF") {
					t.Errorf("parseFailureBackoff(%q) error = %q, want it to name DAEMON_FAILURE_BACKOFF", tt.raw, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFailureBackoff(%q) unexpected error: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("parseFailureBackoff(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

// TestParseBreakerThreshold pins DAEMON_BREAKER_THRESHOLD's parse step:
// lib/env-schema.nix declares it intKind = "positive", so anything else —
// unparsable, zero, or negative — is rejected.
func TestParseBreakerThreshold(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int
		wantErr bool
	}{
		{name: "shipped default", raw: "5", want: 5},
		{name: "one", raw: "1", want: 1},
		{name: "zero rejected", raw: "0", wantErr: true},
		{name: "negative rejected", raw: "-1", wantErr: true},
		{name: "non-numeric rejected", raw: "many", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseBreakerThreshold(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseBreakerThreshold(%q) = %d, nil; want an error", tt.raw, got)
				}
				if !strings.Contains(err.Error(), "DAEMON_BREAKER_THRESHOLD") {
					t.Errorf("parseBreakerThreshold(%q) error = %q, want it to name DAEMON_BREAKER_THRESHOLD", tt.raw, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseBreakerThreshold(%q) unexpected error: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("parseBreakerThreshold(%q) = %d, want %d", tt.raw, got, tt.want)
			}
		})
	}
}

// TestParseBreakerWindow pins DAEMON_BREAKER_WINDOW's parse step: a Go
// duration string, rejected unless strictly positive.
func TestParseBreakerWindow(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{name: "shipped default", raw: "15m", want: 15 * time.Minute},
		{name: "malformed rejected", raw: "not-a-duration", wantErr: true},
		{name: "zero rejected", raw: "0s", wantErr: true},
		{name: "negative rejected", raw: "-1m", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseBreakerWindow(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseBreakerWindow(%q) = %v, nil; want an error", tt.raw, got)
				}
				if !strings.Contains(err.Error(), "DAEMON_BREAKER_WINDOW") {
					t.Errorf("parseBreakerWindow(%q) error = %q, want it to name DAEMON_BREAKER_WINDOW", tt.raw, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseBreakerWindow(%q) unexpected error: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("parseBreakerWindow(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

// TestValidateSignalCarrier pins BOX_SIGNAL_CARRIER's startup check: unset is
// valid (the knob is env-only, so an absent value means "let the child
// take the schema default"), and the two schema choices are valid; anything
// else is rejected by name.
func TestValidateSignalCarrier(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "log", raw: "log"},
		{name: "socket", raw: "socket"},
		{name: "unset", raw: ""},
		{name: "bogus rejected", raw: "carrier-pigeon", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSignalCarrier(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("validateSignalCarrier(%q) = nil; want an error", tt.raw)
				}
				if !strings.Contains(err.Error(), "BOX_SIGNAL_CARRIER") {
					t.Errorf("validateSignalCarrier(%q) error = %q, want it to name BOX_SIGNAL_CARRIER", tt.raw, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateSignalCarrier(%q) unexpected error: %v", tt.raw, err)
			}
		})
	}
}

// TestMainRun_BadDaemonIdleCapFailsStartup is the end-to-end case: an
// --input document whose DAEMON_IDLE_CAP sits below DAEMON_IDLE_FLOOR must
// refuse the start before any slot, any claim, any Box — exit 1, with
// stderr naming both knobs, the same seam TestMainRun_BadAwakeWindow uses
// for its own knob.
func TestMainRun_BadDaemonIdleCapFailsStartup(t *testing.T) {
	clearKnobEnvT(t)
	docPath := writeInputDocT(t, map[string]string{
		"DAEMON_APP":        ".#dogfood",
		"BASE_BRANCH":       "main",
		"MAX_PARALLEL":      "1",
		"DAEMON_IDLE_FLOOR": "5m",
		"DAEMON_IDLE_CAP":   "1m",
	})

	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"--input", docPath, "dispatch"}, &stdout, &stderr)
	if got != 1 {
		t.Fatalf("mainRun() = %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "DAEMON_IDLE_CAP") || !strings.Contains(stderr.String(), "DAEMON_IDLE_FLOOR") {
		t.Errorf("stderr = %q, want it to name both DAEMON_IDLE_CAP and DAEMON_IDLE_FLOOR", stderr.String())
	}
}

// TestMainRun_BadSignalCarrierFailsStartup is the end-to-end case: a
// BOX_SIGNAL_CARRIER value outside the schema's choices must refuse the
// start before any child is spawned — exit 1, with stderr naming the knob,
// the same seam TestMainRun_BadDaemonIdleCapFailsStartup uses for its own
// knobs.
func TestMainRun_BadSignalCarrierFailsStartup(t *testing.T) {
	clearKnobEnvT(t)
	t.Setenv("BOX_SIGNAL_CARRIER", "carrier-pigeon")
	docPath := writeInputDocT(t, map[string]string{
		"DAEMON_APP":   ".#dogfood",
		"BASE_BRANCH":  "main",
		"MAX_PARALLEL": "1",
	})

	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"--input", docPath, "dispatch"}, &stdout, &stderr)
	if got != 1 {
		t.Fatalf("mainRun() = %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "BOX_SIGNAL_CARRIER") {
		t.Errorf("stderr = %q, want it to name BOX_SIGNAL_CARRIER", stderr.String())
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

// TestAnnounceStop asserts the first latch emits exactly the
// shutdown/ShutdownDrain event, cancels the startup context, and closes the
// returned pool Stop — with the event visible in the emitted stream before
// that close is observed (asserted from inside a goroutine that reads the
// buffer only once poolStop has closed, not merely that both happened) —
// and the second latch emits ShutdownEscalate then closes poolAbort, same
// ordering. announceStop replaces handleStopSignals (issue #3626): the
// shared stopsignal relay now owns the signal wiring, this function only
// re-derives the pool's own Stop/Abort pair from it.
func TestAnnounceStop(t *testing.T) {
	stop := make(chan struct{})
	abort := make(chan struct{})
	quit := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var buf bytes.Buffer
	em := daemon.NewEmitter(&buf, time.Now)

	poolStop, poolAbort := announceStop(stop, abort, quit, cancel, em)

	// No lock needed around buf: announceStop's em.Emit happens strictly
	// before its close(stopCh) in program order, and a channel close
	// happens-before the receive it unblocks, so <-poolStop already
	// orders this read after that write under the Go memory model.
	drainSeen := make(chan string, 1)
	go func() {
		<-poolStop
		drainSeen <- buf.String()
	}()

	close(stop)

	var drainAtClose string
	select {
	case drainAtClose = <-drainSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("poolStop never closed after the first latch")
	}
	if ctx.Err() == nil {
		t.Error("ctx.Err() = nil, want the startup context cancelled on the first latch")
	}
	lines := strings.Split(strings.TrimSpace(drainAtClose), "\n")
	if len(lines) != 1 {
		t.Fatalf("event stream at poolStop close = %d lines, want 1: %q", len(lines), drainAtClose)
	}
	var drain daemon.Event
	if err := json.Unmarshal([]byte(lines[0]), &drain); err != nil {
		t.Fatalf("decode drain event %q: %v", lines[0], err)
	}
	if drain.Event != "shutdown" || drain.Reason != daemon.ShutdownDrain {
		t.Errorf("first event = %+v, want event=shutdown reason=%q", drain, daemon.ShutdownDrain)
	}

	escalateSeen := make(chan string, 1)
	go func() {
		<-poolAbort
		escalateSeen <- buf.String()
	}()

	close(abort)

	var full string
	select {
	case full = <-escalateSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("poolAbort never closed after the second latch")
	}
	lines = strings.Split(strings.TrimSpace(full), "\n")
	if len(lines) != 2 {
		t.Fatalf("event stream at poolAbort close = %d lines, want 2: %q", len(lines), full)
	}
	var escalate daemon.Event
	if err := json.Unmarshal([]byte(lines[1]), &escalate); err != nil {
		t.Fatalf("decode escalate event %q: %v", lines[1], err)
	}
	if escalate.Event != "shutdown" || escalate.Reason != daemon.ShutdownEscalate {
		t.Errorf("second event = %+v, want event=shutdown reason=%q", escalate, daemon.ShutdownEscalate)
	}
}

// TestAnnounceStop_QuitBeforeFirstLatch asserts that closing quit before any
// latch fires unparks announceStop's goroutine rather than leaving it
// blocked on <-stop forever — the leak mainRun's repeated test-driving would
// otherwise accumulate one goroutine per call (issue #3546, carried to
// announceStop by #3626).
func TestAnnounceStop_QuitBeforeFirstLatch(t *testing.T) {
	stop := make(chan struct{})
	abort := make(chan struct{})
	quit := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var buf bytes.Buffer
	em := daemon.NewEmitter(&buf, time.Now)

	poolStop, poolAbort := announceStop(stop, abort, quit, cancel, em)

	close(quit)

	select {
	case <-poolStop:
		t.Error("poolStop closed after quit with no latch, want it to stay open")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case <-poolAbort:
		t.Error("poolAbort closed after quit with no latch, want it to stay open")
	case <-time.After(50 * time.Millisecond):
	}
	if ctx.Err() != nil {
		t.Errorf("ctx.Err() = %v, want nil: quit before a latch must not cancel", ctx.Err())
	}
	if buf.Len() != 0 {
		t.Errorf("event stream = %q, want empty", buf.String())
	}
}

// TestAnnounceStop_QuitAfterFirstLatch asserts that closing quit after the
// first latch but before the second still unparks the goroutine, with the
// first latch's cancel/close/shutdown-event side effects already applied
// and no escalation.
func TestAnnounceStop_QuitAfterFirstLatch(t *testing.T) {
	stop := make(chan struct{})
	abort := make(chan struct{})
	quit := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var buf bytes.Buffer
	em := daemon.NewEmitter(&buf, time.Now)

	poolStop, poolAbort := announceStop(stop, abort, quit, cancel, em)

	close(stop)

	select {
	case <-poolStop:
	case <-time.After(5 * time.Second):
		t.Fatal("poolStop never closed after the first latch")
	}
	close(quit)

	select {
	case <-poolAbort:
		t.Error("poolAbort closed after quit with no second latch, want it to stay open")
	case <-time.After(50 * time.Millisecond):
	}
	if ctx.Err() == nil {
		t.Error("ctx.Err() = nil, want the startup context cancelled from the first latch")
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

// writeInputDocT writes an inputdoc.Document with the given settings to a
// fresh file in t.TempDir() and returns its path. The five backoff/breaker
// knobs are filled in at their lib/env-schema.nix defaults unless settings
// already names one, so a caller testing something else (e.g. the
// RESEARCH_RESERVATION checks below) doesn't also have to carry them.
func writeInputDocT(t *testing.T, settings map[string]string) string {
	t.Helper()
	merged := maps.Clone(shippedKnobDefaults)
	maps.Copy(merged, settings)
	data, err := json.Marshal(inputdoc.Document{Settings: merged})
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
	for _, v := range []string{
		"DAEMON_APP", "BASE_BRANCH", "MAX_PARALLEL", "RESEARCH_RESERVATION",
		"DAEMON_IDLE_FLOOR", "DAEMON_IDLE_CAP", "DAEMON_FAILURE_BACKOFF",
		"DAEMON_BREAKER_THRESHOLD", "DAEMON_BREAKER_WINDOW", "BOX_SIGNAL_CARRIER",
	} {
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
	settings := map[string]string{
		"DAEMON_APP":           ".#dogfood",
		"BASE_BRANCH":          "main",
		"MAX_PARALLEL":         "1",
		"RESEARCH_RESERVATION": "0",
	}
	maps.Copy(settings, shippedKnobDefaults)
	doc := inputdoc.Document{Settings: settings}
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

// TestExitSelfChanged_OutsideChildExitBand pins the invariant daemon.ExitSelfChanged's
// own doc comment claims but nothing enforces: it must fall outside the 0-7
// band a *child* launcher's exit codes occupy (daemon.Interpret), so an
// operator restarting a service unit on daemon.ExitSelfChanged alone can never be
// triggered by a child's exit leaking through. Fails if daemon.ExitSelfChanged is
// ever lowered into that band.
func TestExitSelfChanged_OutsideChildExitBand(t *testing.T) {
	for exit := 0; exit < 256; exit++ {
		outcome, action, _ := daemon.Interpret(exit, false)
		// The band is two things, and neither alone is all of it: the 0-7 span
		// the doc names (exit 1 is a child's generic failure, which Interpret
		// leaves unclassified), plus every code Interpret *does* classify, so a
		// `case 10:` added there later collides here instead of silently.
		if exit > 7 && action == daemon.Backoff {
			continue
		}
		if daemon.ExitSelfChanged == exit {
			t.Fatalf("daemon.ExitSelfChanged (%d) collides with child exit code %d, which daemon.Interpret classifies as %q",
				daemon.ExitSelfChanged, exit, outcome)
		}
	}
}

// fakePreflightRunner is startupPreflight's test seam: a minimal
// preflightRunner that never shells out, scripted per test.
type fakePreflightRunner struct {
	revision   string
	fetchErr   error
	doctorExit int
	doctorErr  error

	mu          sync.Mutex
	doctorCalls int
}

func (f *fakePreflightRunner) fetchRevision(ctx context.Context) (string, error) {
	if f.fetchErr != nil {
		return "", f.fetchErr
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

	h := startupPreflight(context.Background(), r, em)
	if h.Class != daemon.HaltNone {
		t.Fatalf("startupPreflight() = %+v, want the zero Halt", h)
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

	h := startupPreflight(context.Background(), r, em)
	if h.Class != daemon.HaltPreflight {
		t.Fatalf("startupPreflight().Class = %v, want %v", h.Class, daemon.HaltPreflight)
	}
	reason := h.String()
	if !strings.Contains(reason, "required triage labels are missing") {
		t.Errorf("reason = %q, want it to name what failed", reason)
	}
	if !strings.Contains(reason, "create the four triage labels") {
		t.Errorf("reason = %q, want it to carry the remedy", reason)
	}

	// startupPreflight itself emits only the "preflight" event now; the
	// matching "halt" event is mainRun's finish helper's job, not
	// exercised by this direct call.
	events := decodePreflightEvents(t, &buf)
	if len(events) != 1 {
		t.Fatalf("events = %+v, want exactly one preflight event", events)
	}
	if events[0].Event != "preflight" || events[0].Outcome != "doctor-required-labels-missing" {
		t.Errorf("events[0] = %+v, want preflight/doctor-required-labels-missing", events[0])
	}
}

// TestStartupPreflight_ConfigInvalid covers doctor exit 2 — the exit an
// undersized podman machine now arrives as (slice 2) — refusing to start.
func TestStartupPreflight_ConfigInvalid(t *testing.T) {
	var buf bytes.Buffer
	em := daemon.NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
	r := &fakePreflightRunner{revision: "deadbeef", doctorExit: 2}

	h := startupPreflight(context.Background(), r, em)
	if h.Class != daemon.HaltPreflight {
		t.Fatalf("startupPreflight().Class = %v, want %v (a refusal for doctor exit 2)", h.Class, daemon.HaltPreflight)
	}
}

// TestStartupPreflight_Connectivity covers doctor exit 3 refusing to start.
func TestStartupPreflight_Connectivity(t *testing.T) {
	var buf bytes.Buffer
	em := daemon.NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
	r := &fakePreflightRunner{revision: "deadbeef", doctorExit: 3}

	h := startupPreflight(context.Background(), r, em)
	if h.Class != daemon.HaltPreflight {
		t.Fatalf("startupPreflight().Class = %v, want %v (a refusal for doctor exit 3)", h.Class, daemon.HaltPreflight)
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

// TestStartupPreflight_FetchRevisionFailure: a preflight that cannot run
// is not a preflight that passed.
func TestStartupPreflight_FetchRevisionFailure(t *testing.T) {
	var buf bytes.Buffer
	em := daemon.NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
	r := &fakePreflightRunner{fetchErr: errors.New("git fetch boom")}

	h := startupPreflight(context.Background(), r, em)
	if h.Class != daemon.HaltPreflight {
		t.Fatalf("startupPreflight().Class = %v, want %v (a refusal on a resolve failure)", h.Class, daemon.HaltPreflight)
	}
	if r.calls() != 0 {
		t.Errorf("RunDoctor called %d times, want 0 (never reached)", r.calls())
	}

	// The matching "halt" event is mainRun's finish helper's job now, not
	// exercised by this direct call — see TestStartupPreflight_RequiredLabelsMissing.
	events := decodePreflightEvents(t, &buf)
	if len(events) != 1 {
		t.Fatalf("events = %+v, want exactly one preflight error event", events)
	}
	if events[0].Event != "preflight" || events[0].Outcome != "doctor-seam-error" {
		t.Errorf("events[0] = %+v, want preflight/doctor-seam-error", events[0])
	}
}

// TestStartupPreflight_NeverEvaluatesSelfPath is the regression pinned by
// the #3625 review finding: startupPreflight only needs a revision to pin
// doctor to, and must never reach ResolveTip's self `nix eval` half even
// with the self check configured (selfAttr set). Driven against a real
// *hostRunner — not fakePreflightRunner — because the bug was in
// preflightRunner's shape (ResolveTip, not fetchRevision), which a fake
// satisfying the narrowed interface can no longer exercise; only the real
// runner's ResolveTip has an eval half to wrongly reach. runnerEvalCommand
// fails were it ever invoked, so the assertion is "0 calls", not "calls
// succeeded" — the finding was that a failing self-eval hard-refused
// startup, so a stub that quietly passes would hide exactly that.
func TestStartupPreflight_NeverEvaluatesSelfPath(t *testing.T) {
	origFetch, origEval, origDoctor := runnerFetchCommand, runnerEvalCommand, runnerDoctorCommand
	t.Cleanup(func() { runnerFetchCommand, runnerEvalCommand, runnerDoctorCommand = origFetch, origEval, origDoctor })

	runnerFetchCommand = scriptedFetchSeamT(t, func() string { return "deadbeef" }, nil)
	var evalCalls int
	runnerEvalCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		evalCalls++
		return exec.CommandContext(ctx, "/bin/sh", "-c", "echo 'nix eval should never run in the preflight' >&2; exit 1")
	}
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})

	var buf bytes.Buffer
	em := daemon.NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
	h := startupPreflight(context.Background(), r, em)
	if h.Class != daemon.HaltNone {
		t.Fatalf("startupPreflight() = %+v, want the zero Halt (self-eval failure must never reach the preflight)", h)
	}
	if evalCalls != 0 {
		t.Errorf("runnerEvalCommand called %d times, want 0", evalCalls)
	}
}

// TestStartupPreflight_RunDoctorSeamFailure covers RunDoctor's own seam
// error (distinct from a classified exit code) refusing to start likewise.
func TestStartupPreflight_RunDoctorSeamFailure(t *testing.T) {
	var buf bytes.Buffer
	em := daemon.NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
	r := &fakePreflightRunner{revision: "deadbeef", doctorErr: errors.New("exec boom")}

	h := startupPreflight(context.Background(), r, em)
	if h.Class != daemon.HaltPreflight {
		t.Fatalf("startupPreflight().Class = %v, want %v (a refusal on a RunDoctor seam failure)", h.Class, daemon.HaltPreflight)
	}
}

// TestStartupPreflight_ContextCancelled covers both seams returning
// ctx.Err(): the result must classify as daemon.HaltOperatorStop, so a
// Ctrl-C during the preflight exits 0 rather than daemon.ExitPreflightFailed.
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
				tt.r.fetchErr = ctx.Err()
			} else {
				tt.r.doctorErr = ctx.Err()
			}

			var buf bytes.Buffer
			em := daemon.NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
			h := startupPreflight(ctx, tt.r, em)
			if h.Class != daemon.HaltOperatorStop {
				t.Fatalf("startupPreflight().Class = %v, want %v", h.Class, daemon.HaltOperatorStop)
			}

			// A cancelled preflight must still reach the durable event
			// stream (every other halt in this daemon does) via its
			// "preflight" event — the matching "halt" event is mainRun's
			// finish helper's job now, not exercised by this direct call.
			events := decodePreflightEvents(t, &buf)
			if len(events) != 1 {
				t.Fatalf("events = %+v, want exactly one preflight event", events)
			}
			if events[0].Event != "preflight" || events[0].Outcome != "doctor-cancelled" {
				t.Errorf("events[0] = %+v, want preflight/doctor-cancelled", events[0])
			}
		})
	}
}

// TestExitPreflightFailed_DistinctFromOtherExitCodes guards daemon.ExitPreflightFailed
// against ever colliding with one of this binary's other exit codes, so a
// later edit cannot make an operator-stop or a self-changed halt
// indistinguishable from a refused start.
func TestExitPreflightFailed_DistinctFromOtherExitCodes(t *testing.T) {
	for _, other := range []int{0, 1, daemon.ExitSelfChanged} {
		if daemon.ExitPreflightFailed == other {
			t.Fatalf("daemon.ExitPreflightFailed (%d) collides with exit code %d", daemon.ExitPreflightFailed, other)
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

	h := startupPreflight(ctx, r, em)
	if h.Class != daemon.HaltOperatorStop {
		t.Fatalf("startupPreflight().Class = %v, want %v (an operator stop)", h.Class, daemon.HaltOperatorStop)
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

// TestFinish_OnePathForEveryPreLoopHalt pins mainRun's single finish path
// (issue #3622 slice 3): a preflight refusal, an operator Ctrl-C during the
// preflight, and an instance-lock refusal each emit exactly one "halt"
// event carrying h.String()'s documented reason and return the exit code
// their HaltClass derives — 11, 0, and 1 respectively — and only the first
// two (sw != nil) publish a halted status file; the instance-lock refusal
// (sw == nil, per finish's doc) leaves no status file behind.
func TestFinish_OnePathForEveryPreLoopHalt(t *testing.T) {
	countHalts := func(t *testing.T, buf *bytes.Buffer, h daemon.Halt) int {
		t.Helper()
		var halts int
		for _, ev := range decodePreflightEvents(t, buf) {
			if ev.Event != "halt" {
				continue
			}
			halts++
			if ev.Reason != h.String() {
				t.Errorf("halt reason = %q, want %q", ev.Reason, h.String())
			}
		}
		return halts
	}

	t.Run("preflight refusal", func(t *testing.T) {
		var buf, stderr bytes.Buffer
		em := daemon.NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
		r := &fakePreflightRunner{revision: "deadbeef", doctorExit: 4}
		h := startupPreflight(context.Background(), r, em)

		dir := t.TempDir()
		sw := daemon.NewStatusWriter(dir, func() time.Time { return time.Unix(0, 0).UTC() })
		if got := finish(&stderr, em, sw, []daemon.Kind{daemon.KindDispatch}, h); got != daemon.ExitPreflightFailed {
			t.Errorf("finish() = %d, want %d", got, daemon.ExitPreflightFailed)
		}
		if got := countHalts(t, &buf, h); got != 1 {
			t.Errorf("halt events = %d, want exactly 1", got)
		}

		report, err := daemon.ReadStatus(dir)
		if err != nil {
			t.Fatalf("ReadStatus: %v", err)
		}
		if report.Status == nil || report.Status.Reason != h.String() {
			t.Errorf("status = %+v, want a halted status naming %q", report.Status, h.String())
		}
	})

	t.Run("operator cancel during preflight", func(t *testing.T) {
		var buf, stderr bytes.Buffer
		em := daemon.NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		h := startupPreflight(ctx, &fakePreflightRunner{}, em)

		dir := t.TempDir()
		sw := daemon.NewStatusWriter(dir, func() time.Time { return time.Unix(0, 0).UTC() })
		if got := finish(&stderr, em, sw, []daemon.Kind{daemon.KindDispatch}, h); got != 0 {
			t.Errorf("finish() = %d, want 0", got)
		}
		if got := countHalts(t, &buf, h); got != 1 {
			t.Errorf("halt events = %d, want exactly 1", got)
		}
	})

	t.Run("instance lock refusal", func(t *testing.T) {
		var buf, stderr bytes.Buffer
		em := daemon.NewEmitter(&buf, func() time.Time { return time.Unix(0, 0).UTC() })
		h := daemon.Halt{Class: daemon.HaltInstanceLock, Detail: "held by pid 123"}

		dir := t.TempDir()
		if got := finish(&stderr, em, nil, []daemon.Kind{daemon.KindDispatch}, h); got != 1 {
			t.Errorf("finish() = %d, want 1", got)
		}
		if got := countHalts(t, &buf, h); got != 1 {
			t.Errorf("halt events = %d, want exactly 1", got)
		}

		report, err := daemon.ReadStatus(dir)
		if err != nil {
			t.Fatalf("ReadStatus: %v", err)
		}
		if report.Status != nil {
			t.Errorf("status = %+v, want no status file written (sw == nil)", report.Status)
		}
	})
}

// TestSettingsKeys pins settingsKeys' sorted-order contract and its nil
// handling: a nil doc and a doc with a nil Settings map must both come back
// as an empty slice rather than panicking (JSON with no "settings" key
// unmarshals to a nil map, not an empty one).
func TestSettingsKeys(t *testing.T) {
	if got := settingsKeys(nil); len(got) != 0 {
		t.Errorf("settingsKeys(nil) = %v, want empty", got)
	}
	if got := settingsKeys(&inputdoc.Document{}); len(got) != 0 {
		t.Errorf("settingsKeys(&inputdoc.Document{}) = %v, want empty", got)
	}
	doc := &inputdoc.Document{Settings: map[string]string{
		"MODEL":        "opus",
		"BASE_BRANCH":  "main",
		"MAX_PARALLEL": "1",
	}}
	got := settingsKeys(doc)
	want := []string{"BASE_BRANCH", "MAX_PARALLEL", "MODEL"}
	if !slices.Equal(got, want) {
		t.Errorf("settingsKeys(doc) = %v, want %v (sorted)", got, want)
	}
}

// TestWarnStrippedChildEnv_SetKnobWarnsExactlyOnce asserts an exported knob
// present in keys produces exactly one "not forwarded to children" line
// naming it — not zero, not a duplicate.
func TestWarnStrippedChildEnv_SetKnobWarnsExactlyOnce(t *testing.T) {
	t.Setenv("MODEL", "opus")
	var stderr bytes.Buffer
	warnStrippedChildEnv([]string{"MODEL"}, &stderr)
	got := strings.Count(stderr.String(), "not forwarded to children")
	if got != 1 {
		t.Errorf("warning count = %d, want 1; stderr=%q", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "MODEL=opus") {
		t.Errorf("stderr = %q, want it to name MODEL=opus", stderr.String())
	}
}

// TestWarnStrippedChildEnv_UnsetKnobWarnsNever asserts a key genuinely
// absent from the environment (not exported at all) produces no line at
// all. t.Setenv registers the restore cleanup; the immediate os.Unsetenv
// then makes the key actually absent for the test body, since t.Setenv
// itself only ever exports (see EmptyExportWarnsNever below for the
// exported-but-empty case).
func TestWarnStrippedChildEnv_UnsetKnobWarnsNever(t *testing.T) {
	t.Setenv("MODEL", "x")
	os.Unsetenv("MODEL")
	var stderr bytes.Buffer
	warnStrippedChildEnv([]string{"MODEL"}, &stderr)
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty (MODEL not set)", stderr.String())
	}
}

// TestWarnStrippedChildEnv_EmptyExportWarnsNever mirrors the "set" test
// lookupKnob itself applies (its `os.Getenv(envVar) != ""` guard in
// main.go): an exported-but-empty knob (present in the environment with
// value "") is not "set" there, so it must not be "set" here either.
func TestWarnStrippedChildEnv_EmptyExportWarnsNever(t *testing.T) {
	t.Setenv("ISSUE_NUMBER", "")
	var stderr bytes.Buffer
	warnStrippedChildEnv([]string{"ISSUE_NUMBER"}, &stderr)
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty (ISSUE_NUMBER exported empty)", stderr.String())
	}
}

// TestWarnStrippedChildEnv_MultipleKeysSortedOrder asserts multiple set
// knobs are warned about in the order keys arrives in (settingsKeys already
// sorts, so this pins that warnStrippedChildEnv does not itself reorder).
func TestWarnStrippedChildEnv_MultipleKeysSortedOrder(t *testing.T) {
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("MODEL", "opus")
	var stderr bytes.Buffer
	warnStrippedChildEnv([]string{"BASE_BRANCH", "MODEL"}, &stderr)
	out := stderr.String()
	iBase := strings.Index(out, "BASE_BRANCH=")
	iModel := strings.Index(out, "MODEL=")
	if iBase == -1 || iModel == -1 {
		t.Fatalf("stderr = %q, want both BASE_BRANCH and MODEL warnings", out)
	}
	if iBase >= iModel {
		t.Errorf("stderr = %q, want BASE_BRANCH warning before MODEL warning", out)
	}
}

// TestMainRun_StrippedEnvWarningPrecedesPreflight asserts the startup
// warning for a stripped knob appears in stderr before mainRun reaches any
// later startup diagnostic. There is no seam onto the real preflight here
// (it needs a real nix/doctor run), so this uses repoRoot's own fast-failing
// "not a git checkout" as the ordering anchor — the same proxy
// TestMainRun_ValidAwakeWindowReachesRepoRoot uses — which is itself well
// before the startupPreflight call in mainRun.
func TestMainRun_StrippedEnvWarningPrecedesPreflight(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	clearKnobEnvT(t)
	t.Setenv("MODEL", "opus")
	doc := validKnobDocument()
	doc.Settings["MODEL"] = "opus"
	path := writeInputDocument(t, doc)

	dir := t.TempDir()
	t.Chdir(dir)

	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"--input", path, "dispatch"}, &stdout, &stderr)
	if got != 1 {
		t.Errorf("mainRun() = %d, want 1", got)
	}
	out := stderr.String()
	iWarn := strings.Index(out, "MODEL=opus set in environment — not forwarded to children")
	iGit := strings.Index(out, "not a git checkout")
	if iWarn == -1 {
		t.Fatalf("stderr = %q, want the stripped-env warning", out)
	}
	if iGit == -1 {
		t.Fatalf("stderr = %q, want the repoRoot failure (ordering anchor)", out)
	}
	if iWarn >= iGit {
		t.Errorf("stderr = %q, want the warning before the repoRoot failure", out)
	}
}

// TestMainRun_NoKnobsSetNoWarning asserts a document with no matching
// ambient env produces none of the stripped-env warning lines.
func TestMainRun_NoKnobsSetNoWarning(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	clearKnobEnvT(t)
	doc := validKnobDocument()
	path := writeInputDocument(t, doc)

	dir := t.TempDir()
	t.Chdir(dir)

	var stdout, stderr bytes.Buffer
	mainRun([]string{"--input", path, "dispatch"}, &stdout, &stderr)
	if strings.Contains(stderr.String(), "not forwarded to children") {
		t.Errorf("stderr = %q, want no stripped-env warning", stderr.String())
	}
}

// bareOriginConsumerT sets up a bare origin repo plus a clone with one
// commit pushed to main, and returns the clone's path. Both
// TestMainRun_*ChildGetsCapturedEnv tests need a real git remote so
// ResolveTip's `git fetch` succeeds during mainRun's preflight.
func bareOriginConsumerT(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	dirConsumer := filepath.Join(root, "consumer")
	gitRunT(t, "", "init", "--bare", bare)
	gitRunT(t, "", "clone", bare, dirConsumer)
	gitRunT(t, dirConsumer, "checkout", "-B", "main")
	gitRunT(t, dirConsumer, "config", "user.email", "consumer@example.com")
	gitRunT(t, dirConsumer, "config", "user.name", "Consumer")
	writeFileT(t, filepath.Join(dirConsumer, "a.txt"), "a\n")
	gitRunT(t, dirConsumer, "add", "a.txt")
	gitRunT(t, dirConsumer, "commit", "-m", "base")
	gitRunT(t, dirConsumer, "push", "-u", "origin", "main")
	return dirConsumer
}

// assertCapturedEnvT checks env for a PATH and HOME entry (proving the
// child inherited the parent's environment, not an empty one) and asserts
// MODEL was stripped (it names a settings key, not a passthrough var).
// label distinguishes the doctor and dispatch child in failure output.
//
// Failure output names keys only, never env itself: env is the live
// os.Environ()-derived child environment, so a %v of it would put real
// credentials into the test log and any transcript that captures it.
func assertCapturedEnvT(t *testing.T, label string, env []string) {
	t.Helper()
	var hasPath, hasHome, hasModel bool
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "PATH="):
			hasPath = true
		case strings.HasPrefix(kv, "HOME="):
			hasHome = true
		case strings.HasPrefix(kv, "MODEL="):
			hasModel = true
		}
	}
	// Only built on a failing assertion, and keeping a no-"=" entry (as
	// itself) rather than dropping it: env is the live, credential-bearing
	// child environment, so this stays off the hot path and never hides a key.
	envKeysT := func() []string {
		keys := make([]string, 0, len(env))
		for _, kv := range env {
			k, _, _ := strings.Cut(kv, "=")
			keys = append(keys, k)
		}
		return keys
	}
	if !hasPath {
		t.Errorf("%s Env keys = %v, want a PATH entry", label, envKeysT())
	}
	if !hasHome {
		t.Errorf("%s Env keys = %v, want a HOME entry", label, envKeysT())
	}
	if hasModel {
		t.Errorf("%s Env keys = %v, want MODEL stripped (it names a settings key)", label, envKeysT())
	}
}

// capturedEnvFixtureT is the shared setup for TestMainRun_*ChildGetsCapturedEnv:
// skip without git, a synthetic HOME (so the fixture never depends on the
// ambient test environment), a cleared+MODEL-set knob env, a bare-origin
// consumer repo, and an input document carrying MODEL plus extra, chdir'd
// into the consumer. Returns the input document path; each caller keeps its
// own doctor/exec stubs and assertions, which diverge between the tests.
func capturedEnvFixtureT(t *testing.T, extra map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("HOME", t.TempDir())

	clearKnobEnvT(t)
	t.Setenv("MODEL", "opus")

	dirConsumer := bareOriginConsumerT(t)

	doc := validKnobDocument()
	doc.Settings["MODEL"] = "opus"
	for k, v := range extra {
		doc.Settings[k] = v
	}
	path := writeInputDocument(t, doc)

	t.Chdir(dirConsumer)
	return path
}

// TestMainRun_DoctorChildGetsCapturedEnv is the regression test for the bug
// where newHostRunner's call site never set env/knobs, so RunDoctor exec'd
// children with an EMPTY environment (no PATH, no HOME) — dogfood's doctor
// preflight couldn't even locate `sh`. The stubbed doctor exits non-zero,
// halting mainRun at startupPreflight before daemon.Loop starts, which
// keeps the test bounded to the doctor seam.
func TestMainRun_DoctorChildGetsCapturedEnv(t *testing.T) {
	path := capturedEnvFixtureT(t, nil)

	origDoctor := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = origDoctor })
	var captured *exec.Cmd
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1")
		captured = cmd
		return cmd
	}

	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"--input", path, "dispatch"}, &stdout, &stderr)
	if got != daemon.ExitPreflightFailed {
		t.Fatalf("mainRun() = %d, want %d (daemon.ExitPreflightFailed); stderr = %q", got, daemon.ExitPreflightFailed, stderr.String())
	}
	if captured == nil {
		t.Fatal("runnerDoctorCommand was never called")
	}

	assertCapturedEnvT(t, "doctor child", captured.Env)
}

// TestMainRun_DispatchChildGetsCapturedEnv is the dispatch-child sibling of
// TestMainRun_DoctorChildGetsCapturedEnv above: it stubs the doctor preflight
// to pass, so daemon.Loop actually starts a slot and runs a child through
// runnerExecCommand (runner.go's RunChild seam), and asserts that child gets
// the same captured, knob-stripped environment. Bounding: the stubbed child
// exits 1, an unclassified outcome (Interpret's default case), and
// DAEMON_BREAKER_THRESHOLD is set to 1 (the parser's own minimum) so the
// pool-wide breaker trips on that first failure and Loop halts at once,
// rather than backing the slot off forever.
func TestMainRun_DispatchChildGetsCapturedEnv(t *testing.T) {
	path := capturedEnvFixtureT(t, map[string]string{"DAEMON_BREAKER_THRESHOLD": "1"})

	origDoctor := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = origDoctor })
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}

	origExec := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = origExec })
	// No mutex needed: mainRun calls daemon.Loop synchronously, and Loop
	// joins every slot goroutine at wg.Wait() before returning, so this
	// write happens-before the read below. A mutex here would anyway never
	// have covered cmd.Env, which runner.go sets after this stub returns.
	var captured *exec.Cmd
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		cmd := exec.Command("/bin/sh", "-c", "exit 1")
		captured = cmd
		return cmd
	}

	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"--input", path, "dispatch"}, &stdout, &stderr)
	if got != 1 {
		t.Fatalf("mainRun() = %d, want 1 (breaker halt); stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "breaker_trip") {
		// threshold 1 also trips on a pre-child failure (ResolveTip's fetch,
		// self-build), so this alone doesn't prove a child dispatched — the
		// captured == nil check below carries that claim.
		t.Errorf("stdout = %q, want a breaker_trip event", stdout.String())
	}

	if captured == nil {
		t.Fatal("runnerExecCommand was never called")
	}

	assertCapturedEnvT(t, "dispatch child", captured.Env)
}

// childDrainThenEscalate is the scripted child for
// TestMainRun_EndToEndSignalDrainsThenEscalates: it traps both TERM and
// INT (the daemon only ever sends SIGTERM then SIGINT, forwardSignals'
// own doc), writes a distinct word to the transcript file ($1) per signal
// it has actually received, and exits 7 only on the second — the same
// "operator stopped this child" exit code childExitOnSecondSignal above
// uses, but with a transcript instead of a bare counter so the test can
// tell drain and escalation apart, not just count two signals. `: >"$0"`
// arms the marker (waitForArmed's contract) once the trap is installed.
const childDrainThenEscalate = `
n=0
trap 'n=$((n+1)); if [ "$n" -eq 1 ]; then echo drain >>"$1"; else echo escalate >>"$1"; exit 7; fi' TERM INT
: >"$0"
while :; do sleep 0.05; done`

// waitForFileContains polls path until its contents contain want, bounded
// like waitForArmed's own 2s budget — a plain os.ReadFile poll is enough
// here because the transcript is a small file one shell script appends to,
// not a socket or pipe a reader could observe half-written.
func waitForFileContains(t *testing.T, path, want string) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), want) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s never contained %q", path, want)
}

// TestMainRun_EndToEndSignalDrainsThenEscalates is the one test that
// actually drives mainRun through daemon.Loop with a real child process
// (issue #3626's Seam 2, T7): every other link in the drain/escalate chain
// — the stopsignal relay, announceStop, Config.Stop/Abort, ChildRequest,
// forwardSignals — has its own narrower test elsewhere, but this is the
// only one that wires all of them together and watches a real SIGTERM then
// SIGINT actually reach a real process. It earns its cost (a real git
// checkout, a real child, two real signal deliveries) as the regression
// tripwire for that whole chain: any single broken link here fails this
// test even if every narrower test above it still passes on its own.
//
// It drives the stop/abort latch directly through installStopSignal's test
// seam (never a real signal to the test binary itself — mainRun's own
// argument for the same seam applies here too) and stubs the doctor and
// child exec seams so the daemon needs nothing but a real git checkout with
// a fetchable origin — ResolveTip's own contract (see the package doc
// on bareOriginConsumerT's other callers).
func TestMainRun_EndToEndSignalDrainsThenEscalates(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}
	// Disables the self-change check (SelfPath): unset, this configuration
	// never reaches runnerEvalCommand, so there is nothing else to stub.
	t.Setenv("SPINDRIFT_DAEMON_PROGRAM", "")

	dirConsumer := bareOriginConsumerT(t)

	doc := validKnobDocument()
	// Small enough that nothing in this test could ever wait one out —
	// the scripted child never exits on its own, so idle/failure backoff
	// never actually triggers, but a tiny value keeps that true even if a
	// future change to the flow adds an extra iteration before shutdown.
	doc.Settings["DAEMON_IDLE_FLOOR"] = "1ms"
	doc.Settings["DAEMON_IDLE_CAP"] = "2ms"
	doc.Settings["DAEMON_FAILURE_BACKOFF"] = "0s"
	path := writeInputDocument(t, doc)

	t.Chdir(dirConsumer)

	// The test's own two-channel latch, driven directly rather than through
	// a real OS signal — installStopSignal's whole reason for existing.
	stop := make(chan struct{})
	abort := make(chan struct{})
	origInstall := installStopSignal
	installStopSignal = func() (<-chan struct{}, <-chan struct{}, func()) {
		return stop, abort, func() {}
	}
	t.Cleanup(func() { installStopSignal = origInstall })

	origDoctor := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = origDoctor })
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}

	dir := t.TempDir()
	armed := filepath.Join(dir, "armed")
	transcript := filepath.Join(dir, "transcript")

	origExec := runnerExecCommand
	var mu sync.Mutex
	var childCmd *exec.Cmd
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		childCmd = exec.Command("/bin/sh", "-c", childDrainThenEscalate, armed, transcript)
		return childCmd
	}
	t.Cleanup(func() {
		runnerExecCommand = origExec
		mu.Lock()
		c := childCmd
		mu.Unlock()
		if c != nil && c.Process != nil {
			_ = c.Process.Kill()
		}
	})

	var stdout, stderr bytes.Buffer
	doneCh := make(chan int, 1)
	go func() {
		doneCh <- mainRun([]string{"--input", path, "dispatch"}, &stdout, &stderr)
	}()

	waitForArmed(t, armed)
	close(stop)
	waitForFileContains(t, transcript, "drain")
	close(abort)

	var got int
	select {
	case got = <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("mainRun did not return after stop then abort — the child was signalled but never reaped, or the pool never halted")
	}
	if got != 0 {
		t.Fatalf("mainRun() = %d, want 0; stderr=%s", got, stderr.String())
	}

	data, err := os.ReadFile(transcript)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	gotLines := strings.Split(strings.TrimSpace(string(data)), "\n")
	wantLines := []string{"drain", "escalate"}
	if !slices.Equal(gotLines, wantLines) {
		t.Fatalf("transcript = %v, want %v (drain must reach the child before escalation, never the reverse or a repeat)", gotLines, wantLines)
	}

	var shutdownReasons []string
	var haltSeen bool
	var haltReason string
	for _, line := range strings.Split(stdout.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev daemon.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("decode event line %q: %v", line, err)
		}
		switch ev.Event {
		case "shutdown":
			shutdownReasons = append(shutdownReasons, ev.Reason)
		case "halt":
			haltSeen = true
			haltReason = ev.Reason
		}
	}
	wantShutdowns := []string{daemon.ShutdownDrain, daemon.ShutdownEscalate}
	if !slices.Equal(shutdownReasons, wantShutdowns) {
		t.Errorf("shutdown reasons = %v, want %v", shutdownReasons, wantShutdowns)
	}
	if !haltSeen {
		t.Fatalf("no halt event in the stream: %s", stdout.String())
	}
	// Either reason is a clean stop reaching the same operator-visible
	// fact (exit 0): the pool's own Stop watcher (loop.go) and the slot
	// reading the child's exit 7 while Stop is closed (Interpret) race to
	// record the halt first, and idempotency (pool.halt) means only the
	// winner's reason survives — which one wins is not a contract this
	// test pins, only that whichever it is, it is one of the two clean
	// stops.
	if haltReason != "context-cancelled: stop requested" && haltReason != "outcome: signalled-stop" {
		t.Errorf("halt reason = %q, want %q or %q", haltReason, "context-cancelled: stop requested", "outcome: signalled-stop")
	}
}
