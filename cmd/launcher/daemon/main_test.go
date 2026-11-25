package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/doctor"
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

// newTestEmitter returns an Emitter on w with a fixed clock and discarded
// diagnostics.
func newTestEmitter(w io.Writer) *daemon.Emitter {
	return daemon.NewEmitter(w, io.Discard, func() time.Time { return time.Unix(0, 0).UTC() })
}

// validKnobDocument carries every knob mainRun requires before the
// DAEMON_AWAKE_WINDOW parse (the backoff/breaker ones at their schema
// defaults), so awake-window tests fail for the reason they test.
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
			clearKnobEnvT(t)
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

// TestMainRun_BadKindSelector pins that fail() owns the "daemon: " prefix
// alone: an unknown kind selector's stderr must carry it exactly once, not
// doubled by ParseKinds also prefixing its own error.
func TestMainRun_BadKindSelector(t *testing.T) {
	clearKnobEnvT(t)
	path := writeInputDocument(t, validKnobDocument())
	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"--input", path, "bogus"}, &stdout, &stderr)
	if got != 1 {
		t.Errorf("mainRun() = %d, want 1", got)
	}
	want := `daemon: unknown kind "bogus"` + "\n"
	if stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}

// TestMainRun_AbsentAwakeWindowIsNotAnError asserts an absent
// DAEMON_AWAKE_WINDOW draws no diagnostic of its own; the exit 1 is
// parseArgs' (no --input).
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

// TestMainRun_ValidAwakeWindowReachesRepoRoot asserts a document
// DAEMON_AWAKE_WINDOW parses. With no seam onto daemon.Config, repoRoot's
// "not a git checkout" from a non-git cwd is the proxy for getting past it.
func TestMainRun_ValidAwakeWindowReachesRepoRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	clearKnobEnvT(t)
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

// TestParseSlots pins that an unparsable or non-positive MAX_PARALLEL fails
// startup rather than reaching daemon.Loop's Slots halt, which is for
// programming errors.
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
		name                 string
		args                 []string
		wantPath             string
		wantKinds            []daemon.Kind
		wantExplicitSelector bool
		wantFeatureBranch    string
		wantCheckOnly        bool
		wantRestartInPlace   bool
		wantErr              bool
	}{
		{
			// No positional verb draws from every kind off one pool (issue
			// #3541, #3878) — parseArgs' own default; run's gateKinds is
			// what later drops butler when BUTLER_CHORES is empty.
			name:      "no positional verb defaults to every kind",
			args:      []string{"--input", "/tmp/in.json"},
			wantPath:  "/tmp/in.json",
			wantKinds: []daemon.Kind{daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research), daemon.KindOf(dispatchkind.Butler), daemon.KindOf(dispatchkind.Recover)},
		},
		{
			name:                 "explicit dispatch is work-only",
			args:                 []string{"--input", "/tmp/in.json", "dispatch"},
			wantPath:             "/tmp/in.json",
			wantKinds:            []daemon.Kind{daemon.KindOf(dispatchkind.Work)},
			wantExplicitSelector: true,
		},
		{
			name:                 "explicit research",
			args:                 []string{"--input", "/tmp/in.json", "research"},
			wantPath:             "/tmp/in.json",
			wantKinds:            []daemon.Kind{daemon.KindOf(dispatchkind.Research)},
			wantExplicitSelector: true,
		},
		{
			name:                 "explicit butler",
			args:                 []string{"--input", "/tmp/in.json", "butler"},
			wantPath:             "/tmp/in.json",
			wantKinds:            []daemon.Kind{daemon.KindOf(dispatchkind.Butler)},
			wantExplicitSelector: true,
		},
		{
			name:                 "explicit recover is recover-only",
			args:                 []string{"--input", "/tmp/in.json", "recover"},
			wantPath:             "/tmp/in.json",
			wantKinds:            []daemon.Kind{daemon.KindOf(dispatchkind.Recover)},
			wantExplicitSelector: true,
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
		{
			name:              "feature branch alone",
			args:              []string{"--input", "/tmp/in.json", "--feature-branch", "feature-x"},
			wantPath:          "/tmp/in.json",
			wantKinds:         []daemon.Kind{daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research), daemon.KindOf(dispatchkind.Butler), daemon.KindOf(dispatchkind.Recover)},
			wantFeatureBranch: "feature-x",
		},
		{
			// The flag can precede the kind selector...
			name:                 "feature branch before kind selector",
			args:                 []string{"--input", "/tmp/in.json", "--feature-branch", "feature-x", "dispatch"},
			wantPath:             "/tmp/in.json",
			wantKinds:            []daemon.Kind{daemon.KindOf(dispatchkind.Work)},
			wantExplicitSelector: true,
			wantFeatureBranch:    "feature-x",
		},
		{
			// ...or follow it: parseArgs scans every arg regardless of order,
			// so only --input's position relative to its own value matters.
			name:                 "feature branch after kind selector",
			args:                 []string{"--input", "/tmp/in.json", "dispatch", "--feature-branch", "feature-x"},
			wantPath:             "/tmp/in.json",
			wantKinds:            []daemon.Kind{daemon.KindOf(dispatchkind.Work)},
			wantExplicitSelector: true,
			wantFeatureBranch:    "feature-x",
		},
		{
			name:                 "check-only before kind selector",
			args:                 []string{"--input", "/tmp/in.json", "--check-only", "dispatch"},
			wantPath:             "/tmp/in.json",
			wantKinds:            []daemon.Kind{daemon.KindOf(dispatchkind.Work)},
			wantExplicitSelector: true,
			wantCheckOnly:        true,
		},
		{
			name:                 "check-only after kind selector and feature branch",
			args:                 []string{"--input", "/tmp/in.json", "dispatch", "--feature-branch", "feature-x", "--check-only"},
			wantPath:             "/tmp/in.json",
			wantKinds:            []daemon.Kind{daemon.KindOf(dispatchkind.Work)},
			wantExplicitSelector: true,
			wantFeatureBranch:    "feature-x",
			wantCheckOnly:        true,
		},
		{
			// The default is drain: an in-place restart is strictly opt-in.
			name:                 "restart-in-place is off by default",
			args:                 []string{"--input", "/tmp/in.json", "dispatch"},
			wantPath:             "/tmp/in.json",
			wantKinds:            []daemon.Kind{daemon.KindOf(dispatchkind.Work)},
			wantExplicitSelector: true,
		},
		{
			name:                 "restart-in-place after the selector",
			args:                 []string{"--input", "/tmp/in.json", "dispatch", "--restart-in-place"},
			wantPath:             "/tmp/in.json",
			wantKinds:            []daemon.Kind{daemon.KindOf(dispatchkind.Work)},
			wantExplicitSelector: true,
			wantRestartInPlace:   true,
		},
		{
			name:          "check-only first, with no selector",
			args:          []string{"--check-only", "--input", "/tmp/in.json"},
			wantPath:      "/tmp/in.json",
			wantKinds:     []daemon.Kind{daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research), daemon.KindOf(dispatchkind.Butler), daemon.KindOf(dispatchkind.Recover)},
			wantCheckOnly: true,
		},
		{
			name:    "--feature-branch with no value",
			args:    []string{"--input", "/tmp/in.json", "--feature-branch"},
			wantErr: true,
		},
		{
			name:    "--feature-branch with empty value",
			args:    []string{"--input", "/tmp/in.json", "--feature-branch", ""},
			wantErr: true,
		},
		{
			// A dropped value ("daemon --input p --feature-branch dispatch")
			// must not silently consume the kind selector as the branch name.
			name:    "--feature-branch value equal to dispatch selector rejected",
			args:    []string{"--input", "/tmp/in.json", "--feature-branch", "dispatch"},
			wantErr: true,
		},
		{
			name:    "--feature-branch value equal to research selector rejected",
			args:    []string{"--input", "/tmp/in.json", "--feature-branch", "research"},
			wantErr: true,
		},
		{
			name:    "--feature-branch value that looks like a flag rejected",
			args:    []string{"--input", "/tmp/in.json", "--feature-branch", "--base-branch"},
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
			if got.InputPath != tt.wantPath || !slices.Equal(got.Kinds, tt.wantKinds) || got.ExplicitSelector != tt.wantExplicitSelector || got.FeatureBranch != tt.wantFeatureBranch || got.CheckOnly != tt.wantCheckOnly || got.RestartInPlace != tt.wantRestartInPlace {
				t.Errorf("parseArgs(%v) = %+v, want {InputPath:%q Kinds:%v ExplicitSelector:%v FeatureBranch:%q CheckOnly:%v RestartInPlace:%v}", tt.args, got, tt.wantPath, tt.wantKinds, tt.wantExplicitSelector, tt.wantFeatureBranch, tt.wantCheckOnly, tt.wantRestartInPlace)
			}
		})
	}
}

// TestGateKinds pins Enablement gating (ADR 0056, issue #3878): a bare
// invocation drops butler without chores, an explicit `butler` fails instead,
// and a butler-free kind set never fails on malformed butler knobs (issue
// #3991). With no chores, malformed BUTLER_EVERY/BUTLER_CHORE_CLASSES are
// never parsed; with one enabled, a chore.Load error fails startup.
func TestGateKinds(t *testing.T) {
	every := []daemon.Kind{daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research), daemon.KindOf(dispatchkind.Butler)}
	recoverKind := daemon.KindOf(dispatchkind.Recover)
	everyWithRecover := append(slices.Clone(every), recoverKind)
	tests := []struct {
		name             string
		kinds            []daemon.Kind
		explicitSelector bool
		butlerChores     string
		butlerEvery      string
		butlerClasses    string
		codeForge        string // resolved CODE_FORGE; "" is not a backend
		want             []daemon.Kind
		wantErr          bool
		wantErrIs        error  // when set, err must wrap it
		wantErrMsg       string // when set, err must contain it
	}{
		{
			name:  "bare default with no chores drops butler",
			kinds: every,
			want:  []daemon.Kind{daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research)},
		},
		{
			name:         "bare default with whitespace-only chores drops butler",
			kinds:        every,
			butlerChores: "  ",
			want:         []daemon.Kind{daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research)},
		},
		{
			name:         "bare default with chores enabled keeps butler",
			kinds:        every,
			butlerChores: "bugs",
			want:         every,
		},
		{
			name:             "explicit butler with no chores fails",
			kinds:            []daemon.Kind{daemon.KindOf(dispatchkind.Butler)},
			explicitSelector: true,
			wantErr:          true,
			wantErrMsg:       "butler selected but",
		},
		{
			name:             "explicit butler with chores enabled keeps it",
			kinds:            []daemon.Kind{daemon.KindOf(dispatchkind.Butler)},
			explicitSelector: true,
			butlerChores:     "bugs",
			want:             []daemon.Kind{daemon.KindOf(dispatchkind.Butler)},
		},
		{
			name:             "kind set without butler is untouched",
			kinds:            []daemon.Kind{daemon.KindOf(dispatchkind.Work)},
			explicitSelector: true,
			want:             []daemon.Kind{daemon.KindOf(dispatchkind.Work)},
		},
		{
			name:        "butler not in kinds with broken BUTLER_EVERY passes untouched",
			kinds:       []daemon.Kind{daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research)},
			butlerEvery: "not-a-duration",
			want:        []daemon.Kind{daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research)},
		},
		{
			name:         "butler in kinds with a Load error fails",
			kinds:        every,
			butlerChores: "bugs",
			butlerEvery:  "not-a-duration",
			wantErr:      true,
		},
		{
			name:          "bare default with empty chores and a stale override drops butler",
			kinds:         every,
			butlerChores:  "",
			butlerEvery:   "6h docs-drift=168h",
			butlerClasses: "",
			want:          []daemon.Kind{daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research)},
		},
		{
			name:         "bare default with empty chores and malformed every drops butler",
			kinds:        every,
			butlerChores: "",
			butlerEvery:  "not-a-duration",
			want:         []daemon.Kind{daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research)},
		},
		{
			name:             "explicit butler selector with empty chores fails on no chores, not the malformed every",
			kinds:            []daemon.Kind{daemon.KindOf(dispatchkind.Butler)},
			explicitSelector: true,
			butlerChores:     "",
			butlerEvery:      "not-a-duration",
			wantErr:          true,
			wantErrIs:        chore.ErrNoChores,
		},
		{
			name:      "bare default keeps recover for github read-only",
			kinds:     everyWithRecover,
			codeForge: "github",
			want:      []daemon.Kind{daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research), recoverKind},
		},
		{
			name:      "bare default keeps recover for forgejo read-only",
			kinds:     everyWithRecover,
			codeForge: "forgejo",
			want:      []daemon.Kind{daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research), recoverKind},
		},
		{
			name:      "bare default drops recover for local read-only",
			kinds:     everyWithRecover,
			codeForge: "local",
			want:      []daemon.Kind{daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research)},
		},
		{
			name:      "bare default drops recover for git read-only",
			kinds:     everyWithRecover,
			codeForge: "git",
			want:      []daemon.Kind{daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research)},
		},
		{
			name:             "explicit recover with relay disabled fails on the sentinel",
			kinds:            []daemon.Kind{recoverKind},
			explicitSelector: true,
			codeForge:        "local",
			wantErr:          true,
			wantErrIs:        errNoOutboxRelay,
			wantErrMsg:       "recover selected but",
		},
		{
			name:             "explicit recover with relay enabled yields just recover",
			kinds:            []daemon.Kind{recoverKind},
			explicitSelector: true,
			codeForge:        "forgejo",
			want:             []daemon.Kind{recoverKind},
		},
		{
			name:             "explicit dispatch excludes recover whatever the relay",
			kinds:            []daemon.Kind{daemon.KindOf(dispatchkind.Work)},
			explicitSelector: true,
			codeForge:        "github",
			want:             []daemon.Kind{daemon.KindOf(dispatchkind.Work)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			knobs := gateKnobs{
				chores:    chore.Knobs{Chores: tt.butlerChores, Every: tt.butlerEvery, Classes: tt.butlerClasses},
				codeForge: tt.codeForge,
			}
			got, err := gateKinds(tt.kinds, tt.explicitSelector, knobs)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("gateKinds(%v, %v, %+v) = %v, nil; want an error", tt.kinds, tt.explicitSelector, knobs, got)
				}
				if tt.wantErrIs != nil && !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("gateKinds(%v, %v, %+v) error = %v; want it to wrap %v", tt.kinds, tt.explicitSelector, knobs, err, tt.wantErrIs)
				}
				if tt.wantErrMsg != "" && !strings.Contains(err.Error(), tt.wantErrMsg) {
					t.Fatalf("gateKinds(%v, %v, %+v) error = %v; want it to contain %q", tt.kinds, tt.explicitSelector, knobs, err, tt.wantErrMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("gateKinds(%v, %v, %+v) unexpected error: %v", tt.kinds, tt.explicitSelector, knobs, err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("gateKinds(%v, %v, %+v) = %v, want %v", tt.kinds, tt.explicitSelector, knobs, got, tt.want)
			}
		})
	}
}

// TestParseResearchReservation pins that an unparsable, negative, or
// over-MAX_PARALLEL RESEARCH_RESERVATION fails startup naming both numbers,
// rather than reaching daemon.Loop's halt.
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

// TestMainRun_BadDaemonIdleCapFailsStartup: a document DAEMON_IDLE_CAP below
// DAEMON_IDLE_FLOOR refuses the start, naming both knobs.
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

// checkOnlyNoNetworkT fails the test if the daemon reaches any process seam
// (doctor, git fetch, exec, eval) that --check-only must never touch. The gh
// demand probe and the token-refresh goroutine have no seam, so the early
// return's position in mainRun (before them) is what keeps them out.
func checkOnlyNoNetworkT(t *testing.T) {
	t.Helper()
	origDoctor, origFetch := runnerDoctorCommand, runnerFetchCommand
	origExec, origEval := runnerExecCommand, runnerEvalCommand
	t.Cleanup(func() {
		runnerDoctorCommand, runnerFetchCommand = origDoctor, origFetch
		runnerExecCommand, runnerEvalCommand = origExec, origEval
	})
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		t.Errorf("--check-only exec'd: %s %v", name, args)
		return exec.Command("/bin/sh", "-c", "exit 1")
	}
	runnerEvalCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		t.Errorf("--check-only evaluated: %s %v", name, args)
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1")
	}
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		t.Errorf("--check-only ran doctor: %s %v", name, args)
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1")
	}
	runnerFetchCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		t.Errorf("--check-only fetched: %s %v", name, args)
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1")
	}
}

// TestMainRun_CheckOnlyValidConfigExitsZero: --check-only parses the input
// document, validates the config, and exits 0 without doctor, fetch, or a
// single event on stdout (the machine-readable stream).
func TestMainRun_CheckOnlyValidConfigExitsZero(t *testing.T) {
	clearKnobEnvT(t)
	checkOnlyNoNetworkT(t)
	docPath := writeInputDocT(t, map[string]string{
		"DAEMON_APP":   ".#dogfood",
		"BASE_BRANCH":  "main",
		"MAX_PARALLEL": "1",
	})

	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"--input", docPath, "--check-only", "dispatch"}, &stdout, &stderr)
	if got != 0 {
		t.Fatalf("mainRun() = %d, want 0; stderr = %q", got, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

// TestMainRun_CheckOnlyIgnoresHeldLock: a freshly built successor runs
// --check-only beside the live daemon that holds the instance lock, so it
// must not take it.
func TestMainRun_CheckOnlyIgnoresHeldLock(t *testing.T) {
	inputPath := lockedCheckoutT(t)
	checkOnlyNoNetworkT(t)

	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"--input", inputPath, "--check-only"}, &stdout, &stderr)
	if got != 0 {
		t.Fatalf("mainRun() = %d, want 0; stderr = %q", got, stderr.String())
	}
	if strings.Contains(stdout.String(), "halt") {
		t.Errorf("stdout = %q, want no halt event", stdout.String())
	}
}

// TestMainRun_CheckOnlyBadConfigMatchesRealStart: a bad config fails
// --check-only with the same diagnostic a real start prints.
func TestMainRun_CheckOnlyBadConfigMatchesRealStart(t *testing.T) {
	clearKnobEnvT(t)
	docPath := writeInputDocT(t, map[string]string{
		"DAEMON_APP":        ".#dogfood",
		"BASE_BRANCH":       "main",
		"MAX_PARALLEL":      "1",
		"DAEMON_IDLE_FLOOR": "5m",
		"DAEMON_IDLE_CAP":   "1m",
	})

	var realOut, realErr, checkOut, checkErr bytes.Buffer
	if got := mainRun([]string{"--input", docPath, "dispatch"}, &realOut, &realErr); got != 1 {
		t.Fatalf("real start mainRun() = %d, want 1", got)
	}
	if got := mainRun([]string{"--input", docPath, "dispatch", "--check-only"}, &checkOut, &checkErr); got != 1 {
		t.Fatalf("--check-only mainRun() = %d, want 1", got)
	}
	if checkErr.String() != realErr.String() {
		t.Errorf("--check-only stderr = %q, want the real start's %q", checkErr.String(), realErr.String())
	}
}

// TestMainRun_CheckOnlyMissingInputDocument: an unreadable --input fails
// --check-only with the same diagnostic a real start prints.
func TestMainRun_CheckOnlyMissingInputDocument(t *testing.T) {
	checkOnlyNoNetworkT(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")

	var realOut, realErr, checkOut, checkErr bytes.Buffer
	if got := mainRun([]string{"--input", missing}, &realOut, &realErr); got != 1 {
		t.Fatalf("real start mainRun() = %d, want 1", got)
	}
	if got := mainRun([]string{"--input", missing, "--check-only"}, &checkOut, &checkErr); got != 1 {
		t.Fatalf("--check-only mainRun() = %d, want 1", got)
	}
	if checkErr.String() != realErr.String() {
		t.Errorf("--check-only stderr = %q, want the real start's %q", checkErr.String(), realErr.String())
	}
	if !strings.Contains(checkErr.String(), "read input document") {
		t.Errorf("stderr = %q, want it to mention the unreadable input document", checkErr.String())
	}
}

// TestMainRun_BadSignalCarrierFailsStartup: a BOX_SIGNAL_CARRIER outside the
// schema's choices refuses the start, naming the knob.
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

// TestAnnounceStop asserts each latch's shutdown event is in the stream
// before its pool channel closes (read only once the close is observed), and
// that the first latch cancels the startup context (issue #3626).
func TestAnnounceStop(t *testing.T) {
	stop := make(chan struct{})
	abort := make(chan struct{})
	quit := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var buf bytes.Buffer
	em := daemon.NewEmitter(&buf, io.Discard, time.Now)

	poolStop, poolAbort := announceStop(stop, abort, quit, cancel, em)

	// No lock around buf: the Emit precedes close(stopCh), and the close
	// happens-before the <-poolStop receive.
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

// TestAnnounceStop_QuitBeforeFirstLatch asserts closing quit before any
// latch unparks announceStop's goroutine; otherwise each mainRun test call
// would leak one (issues #3546, #3626).
func TestAnnounceStop_QuitBeforeFirstLatch(t *testing.T) {
	stop := make(chan struct{})
	abort := make(chan struct{})
	quit := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var buf bytes.Buffer
	em := daemon.NewEmitter(&buf, io.Discard, time.Now)

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
	em := daemon.NewEmitter(&buf, io.Discard, time.Now)

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

// TestMainRun_BadArgv asserts a parseArgs failure's exit code and message
// land on the injected stderr.
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

// writeInputDocT writes an input document with settings and returns its
// path, filling the five backoff/breaker knobs at their schema defaults
// unless settings names them.
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

// daemonKnobEnvVars is every env var the daemon package reads by name, which
// TestClearKnobEnvT_CoversEveryKnob pins to the call sites. Clearing
// SPINDRIFT_DAEMON_PROGRAM keeps the self-change check off. DRIVER is an
// artifact (issue #5050), listed because the guard covers every literal key.
var daemonKnobEnvVars = []string{
	"DAEMON_APP", "BASE_BRANCH", "MAX_PARALLEL", "RESEARCH_RESERVATION",
	"DAEMON_IDLE_FLOOR", "DAEMON_IDLE_CAP", "DAEMON_FAILURE_BACKOFF",
	"DAEMON_BREAKER_THRESHOLD", "DAEMON_BREAKER_WINDOW", "BOX_SIGNAL_CARRIER",
	"MAX_RECOVER_ATTEMPTS", "TRANSIENT_BACKOFF_SECS", "DAEMON_AWAKE_WINDOW",
	"BUTLER_CHORES", "BUTLER_EVERY", "BUTLER_CHORE_CLASSES",
	"DAEMON_SELF_APP", "SPINDRIFT_DAEMON_PROGRAM", "DAEMON_PROBE_INTERVAL",
	"ISSUE_TRACKER", "ISSUE_ROUTE", "LABEL", "IN_PROGRESS_LABEL",
	"COMPLETE_LABEL", "FAILED_LABEL", "REPO_SLUG",
	"SECRET_CMD",
	"CODE_FORGE", "BOX_FORGE_AND_ISSUE_ACCESS", "DRIVER",
}

// clearKnobEnvT clears the daemon's knob env vars for the test, so an ambient
// export cannot shadow the document. Backend knobs are read through the
// Strategies' Lookup, so they are discovered with a recording Lookup (the
// generated ReadKnobs reads every row knob whatever the values).
func clearKnobEnvT(t *testing.T) {
	t.Helper()
	keys := slices.Clone(daemonKnobEnvVars)
	record := func(env string) (string, bool) {
		keys = append(keys, env)
		return "", false
	}
	for _, s := range backendkit.Strategies() {
		if s.ValidateTracker != nil {
			_ = s.ValidateTracker(backendkit.Shared{}, record)
		}
		if s.NewIssueTracker != nil {
			_, _ = s.NewIssueTracker(backendkit.Shared{}, record)
		}
		if s.WebURL != nil {
			_ = s.WebURL(backendkit.Shared{RepoSlug: "o/r"}, record)
		}
		if s.TokenEnvVar != "" {
			keys = append(keys, s.TokenEnvVar+"_CMD")
		}
	}
	// Not in daemonKnobEnvVars: the daemon reads it through rowcredential, not at
	// a literal call site the guard test pins.
	keys = append(keys, "GH_TOKEN_REFRESH_FILE")
	for _, v := range keys {
		t.Setenv(v, "")
	}
}

// An ambient GH_TOKEN_REFRESH_FILE would stand in for the github row's credential.
func TestClearKnobEnvT_ClearsTheRowRefreshFile(t *testing.T) {
	t.Setenv("GH_TOKEN_REFRESH_FILE", "/some/file")
	clearKnobEnvT(t)
	if got := os.Getenv("GH_TOKEN_REFRESH_FILE"); got != "" {
		t.Errorf("GH_TOKEN_REFRESH_FILE = %q after clearKnobEnvT, want it cleared", got)
	}
}

// TestMainRun_WorkOnlyIgnoresExcessiveReservation asserts a `dispatch`
// daemon never validates RESEARCH_RESERVATION: MAX_PARALLEL=1 with
// RESEARCH_RESERVATION=5 would fail if checked, so reaching repoRoot's "not
// a git checkout" proves it was skipped.
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

// TestMainRun_BothKindsValidatesReservation is the multi-kind counterpart:
// the bare selector (butler gated out) must fail at the reservation check
// before repoRoot. With no seam onto daemon.Config, this is where the
// document's RESEARCH_RESERVATION is shown to reach it.
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

// lockedCheckoutT builds a fresh git checkout whose instance lock is held
// in-process, chdirs into it, and returns an input document path, so a
// mainRun call refuses at the lock acquire — no child, no network, no nix.
func lockedCheckoutT(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	clearKnobEnvT(t)
	root := t.TempDir()
	gitRunT(t, root, "-c", "init.defaultBranch=main", "init")

	gitDirPath, err := gitDir(root)
	if err != nil {
		t.Fatalf("gitDir(%q): %v", root, err)
	}

	// Hold the lock ourselves, in-process, standing in for "another daemon
	// already running against this checkout".
	holdCheckoutLockT(t, gitDirPath)

	// A bare invocation draws from both kinds, which makes
	// RESEARCH_RESERVATION a required knob: without it startup fails there,
	// short of the lock acquire these tests exist to exercise.
	inputPath := writeInputDocT(t, map[string]string{
		"DAEMON_APP":           ".#dogfood",
		"BASE_BRANCH":          "main",
		"MAX_PARALLEL":         "1",
		"RESEARCH_RESERVATION": "0",
	})
	t.Chdir(root)
	return inputPath
}

// TestMainRun_InstanceLockRefusal is the acceptance-criterion test: a
// second daemon against a checkout whose lock is already held must refuse
// before ever reaching daemon.Loop (no child, no network, no nix — fully
// deterministic), reporting the refusal both on stderr (naming the holder)
// and as a "halt" event on stdout's durable JSON-lines stream (issue #3543).
func TestMainRun_InstanceLockRefusal(t *testing.T) {
	inputPath := lockedCheckoutT(t)

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

// demandMissingEventsT decodes stdout's JSON-lines stream and returns its
// demand_source_missing events.
func demandMissingEventsT(t *testing.T, stdout string) []daemon.Event {
	t.Helper()
	var got []daemon.Event
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		var ev daemon.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("decode stdout event %q: %v", line, err)
		}
		if ev.Event == "demand_source_missing" {
			got = append(got, ev)
		}
	}
	return got
}

// TestMainRun_LockedCheckoutEmitsNoDemandSourceMissing pins that the warning
// waits for the checkout lock: a refused second daemon never schedules
// anything, so it must not claim a fallback scheduler.
func TestMainRun_LockedCheckoutEmitsNoDemandSourceMissing(t *testing.T) {
	inputPath := lockedCheckoutT(t)

	var stdout, stderr bytes.Buffer
	mainRun([]string{"--input", inputPath}, &stdout, &stderr)
	if evs := demandMissingEventsT(t, stdout.String()); len(evs) != 0 {
		t.Errorf("events = %+v, want no demand_source_missing before the lock is held", evs)
	}
	if strings.Contains(stderr.String(), "Demand source") {
		t.Errorf("stderr = %q, want no Demand source warning", stderr.String())
	}
}

// TestMainRun_DemandSourceMissingReachesEventStream pins that a probed kind
// without a Demand source (no REPO_SLUG on the github tracker) surfaces as a
// demand_source_missing event on mainRun's stdout stream, once the lock is held.
func TestMainRun_DemandSourceMissingReachesEventStream(t *testing.T) {
	path := capturedEnvFixtureT(t, nil)

	origDoctor := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = origDoctor })
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1")
	}

	var stdout, stderr bytes.Buffer
	if got := mainRun([]string{"--input", path, "dispatch"}, &stdout, &stderr); got != daemon.ExitPreflightFailed {
		t.Fatalf("mainRun() = %d, want %d; stderr = %q", got, daemon.ExitPreflightFailed, stderr.String())
	}
	evs := demandMissingEventsT(t, stdout.String())
	if len(evs) != 1 || evs[0].Kind != daemon.KindOf(dispatchkind.Work) {
		t.Fatalf("demand_source_missing events = %+v, want exactly one for the work kind", evs)
	}
	if evs[0].Reason == "" {
		t.Errorf("event = %+v, want a reason", evs[0])
	}
}

// TestMainRun_EventsFileMirrorsStartupWarnings pins that the Events file tee
// is attached before the startup warnings, which emit through the same
// Emitter, or the durable copy misses them.
func TestMainRun_EventsFileMirrorsStartupWarnings(t *testing.T) {
	path := capturedEnvFixtureT(t, nil)

	origDoctor := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = origDoctor })
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1")
	}

	var stdout, stderr bytes.Buffer
	if got := mainRun([]string{"--input", path, "dispatch"}, &stdout, &stderr); got != daemon.ExitPreflightFailed {
		t.Fatalf("mainRun() = %d, want %d; stderr = %q", got, daemon.ExitPreflightFailed, stderr.String())
	}
	gitDirPath, err := gitDir(".")
	if err != nil {
		t.Fatalf("gitDir: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(gitDirPath, "spindrift-daemon.events"))
	if err != nil {
		t.Fatalf("read Events file: %v", err)
	}
	if string(got) != stdout.String() {
		t.Errorf("Events file != stdout\nfile:   %q\nstdout: %q", got, stdout.String())
	}
}

// failingWriter stands in for a closed or full stdout pipe.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// TestMainRun_EventWriteFailureReachesInjectedStderr pins that mainRun hands
// the emitter its injected stderr, not os.Stderr, so a dead event stream is
// diagnosable by whoever supplied the writers (issue #3711).
func TestMainRun_EventWriteFailureReachesInjectedStderr(t *testing.T) {
	inputPath := lockedCheckoutT(t)

	var stderr bytes.Buffer
	if got := mainRun([]string{"--input", inputPath}, failingWriter{}, &stderr); got != 1 {
		t.Errorf("mainRun() = %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "daemon: event stream write failed: broken pipe") {
		t.Errorf("stderr = %q, want the event-stream write failure diagnostic", stderr.String())
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

	holdCheckoutLockT(t, gitDirPath)

	sw := daemon.NewStatusWriter(gitDirPath, time.Now)
	if err := sw.Write(daemon.Status{
		State: daemon.StateWorking,
		Slots: []daemon.SlotStatus{
			{Slot: 0, Busy: true, Kind: daemon.KindOf(dispatchkind.Work), Issues: []string{"101"}},
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

// TestCmdStatus_GarbageStatusFileStillReportsLiveness pins that a held lock
// beside a garbage status file still prints the liveness sentence before
// the exit-1 failure line.
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

	holdCheckoutLockT(t, gitDirPath)

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

// TestSummarizeSlot covers summarizeSlot's forms, including a butler slot
// naming its Chore (issue #3923).
func TestSummarizeSlot(t *testing.T) {
	cases := []struct {
		name string
		in   daemon.SlotStatus
		want string
	}{
		{
			name: "idle",
			in:   daemon.SlotStatus{Slot: 0, Busy: false},
			want: "0:idle",
		},
		{
			name: "busy with issues",
			in:   daemon.SlotStatus{Slot: 1, Busy: true, Kind: daemon.KindOf(dispatchkind.Work), Issues: []string{"101"}},
			want: "1:busy(dispatch #101)",
		},
		{
			name: "busy with no issues",
			in:   daemon.SlotStatus{Slot: 2, Busy: true, Kind: daemon.KindOf(dispatchkind.Work)},
			want: "2:busy(dispatch)",
		},
		{
			name: "busy butler slot names its Chore",
			in:   daemon.SlotStatus{Slot: 3, Busy: true, Kind: daemon.KindOf(dispatchkind.Butler), Chore: "bugs"},
			want: "3:busy(butler bugs)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarizeSlot(tc.in); got != tc.want {
				t.Errorf("summarizeSlot(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSummarizeStatus_LockHeldDistinguishesNoStatusFromUncorrelated pins
// that a held lock with no status file and one with a predecessor's file
// read differently.
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
	if !strings.Contains(got, "not this holder's (pid=1 host=h kind=dispatch started=x)") {
		t.Errorf("summarizeStatus(uncorrelated status) = %q, want it to contrast the leftover with the holder", got)
	}
}

// TestSummarizeStatus_LockHeldWindowReadsAsPredecessorLeftover pins issue
// #3597: a window-shaped report (lock line and status naming the same dead
// pid, Stale set) must still read as a predecessor's leftover.
func TestSummarizeStatus_LockHeldWindowReadsAsPredecessorLeftover(t *testing.T) {
	report := daemon.StatusReport{
		LockHeld:      true,
		Stale:         true,
		HolderPidGone: true,
		Holder:        "pid=4242 host=h kind=dispatch started=x",
		Status:        &daemon.Status{Pid: 4242, Host: "h", State: daemon.StateWorking},
	}
	want := "daemon: a daemon holds this checkout and is still starting; its lock line and the published status file are both a predecessor's leftover (pid=4242, state=working)"
	if got := summarizeStatus(report); got != want {
		t.Errorf("summarizeStatus(window report) = %q, want %q", got, want)
	}
}

// TestSummarizeStatus_CrossHostPidCollisionIsNotWindow pins that a cross-host
// collision report (HolderPidGone unset) keeps the "not this holder's"
// wording.
func TestSummarizeStatus_CrossHostPidCollisionIsNotWindow(t *testing.T) {
	report := daemon.StatusReport{
		LockHeld: true,
		Stale:    true,
		Holder:   "pid=100 host=a kind=dispatch started=x",
		Status:   &daemon.Status{Pid: 100, Host: "b", State: daemon.StateWorking},
	}
	want := "daemon: a daemon holds this checkout, but the published status file is a predecessor's leftover (pid=100, state=working), not this holder's (pid=100 host=a kind=dispatch started=x)"
	if got := summarizeStatus(report); got != want {
		t.Errorf("summarizeStatus(collision report) = %q, want %q", got, want)
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

// TestMainRun_StatusIgnoresWrapperInput pins issue #4060: the `nix run
// .#daemon` wrapper prepends `--input <doc>`, so status must still be found
// behind it — and never load the (here nonexistent) document.
func TestMainRun_StatusIgnoresWrapperInput(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	gitRunT(t, root, "-c", "init.defaultBranch=main", "init")
	t.Chdir(root)

	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"--input", "/nonexistent.json", "status"}, &stdout, &stderr)
	if got != 0 {
		t.Fatalf("mainRun() = %d, want 0; stderr=%q", got, stderr.String())
	}
	var report daemon.StatusReport
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &report); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	if report.Live {
		t.Errorf("report.Live = true, want false")
	}
}

// TestMainRun_StatusIgnoresWrapperInputExtraArgument asserts
// `--input <doc> status dispatch` stays a usage error, not a kind selector.
func TestMainRun_StatusIgnoresWrapperInputExtraArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"--input", "/nonexistent.json", "status", "dispatch"}, &stdout, &stderr)
	if got != 1 {
		t.Errorf("mainRun() = %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "status takes no arguments") {
		t.Errorf("stderr = %q, want a status takes no arguments usage error", stderr.String())
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

// TestExitSelfChanged_OutsideChildExitBand pins daemon.ExitSelfChanged
// outside a child launcher's exit band, so a service unit restarting on it
// alone is never triggered by a child's exit leaking through.
func TestExitSelfChanged_OutsideChildExitBand(t *testing.T) {
	for exit := 0; exit < 256; exit++ {
		outcome, action, _ := daemon.Interpret(exit, false)
		// The band is two things, and neither alone is all of it: the 0-7 span
		// ExitSelfChanged's doc names (exit 1 is a child's generic failure, which Interpret
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
	doctorLine string // the labels line RunDoctor reports alongside doctorExit

	mu          sync.Mutex
	doctorCalls int
}

func (f *fakePreflightRunner) fetchRevision(ctx context.Context) (string, error) {
	if f.fetchErr != nil {
		return "", f.fetchErr
	}
	return f.revision, nil
}

func (f *fakePreflightRunner) RunDoctor(ctx context.Context, revision string) (int, string, error) {
	f.mu.Lock()
	f.doctorCalls++
	f.mu.Unlock()
	if f.doctorErr != nil {
		return 0, "", f.doctorErr
	}
	return f.doctorExit, f.doctorLine, nil
}

func (f *fakePreflightRunner) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.doctorCalls
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
	em := newTestEmitter(&buf)
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
// (missing required labels).
func TestStartupPreflight_RequiredLabelsMissing(t *testing.T) {
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	r := &fakePreflightRunner{revision: "deadbeef", doctorExit: 4}

	h := startupPreflight(context.Background(), r, em)
	if h.Class != daemon.HaltPreflight {
		t.Fatalf("startupPreflight().Class = %v, want %v", h.Class, daemon.HaltPreflight)
	}
	reason := h.String()
	if !strings.Contains(reason, "required labels are missing") {
		t.Errorf("reason = %q, want it to name what failed", reason)
	}
	if !strings.Contains(reason, "create the missing labels") {
		t.Errorf("reason = %q, want it to carry the remedy", reason)
	}

	// The matching "halt" event is finish's, not exercised here.
	events := decodePreflightEvents(t, &buf)
	if len(events) != 1 {
		t.Fatalf("events = %+v, want exactly one preflight event", events)
	}
	if events[0].Event != "preflight" || events[0].Outcome != "doctor-required-labels-missing" {
		t.Errorf("events[0] = %+v, want preflight/doctor-required-labels-missing", events[0])
	}
}

// TestStartupPreflight_ConfigInvalid covers doctor exit 2 (e.g. an
// undersized podman machine) refusing to start.
func TestStartupPreflight_ConfigInvalid(t *testing.T) {
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	r := &fakePreflightRunner{revision: "deadbeef", doctorExit: 2}

	h := startupPreflight(context.Background(), r, em)
	if h.Class != daemon.HaltPreflight {
		t.Fatalf("startupPreflight().Class = %v, want %v (a refusal for doctor exit 2)", h.Class, daemon.HaltPreflight)
	}
}

// TestStartupPreflight_Connectivity covers doctor exit 3 refusing to start.
func TestStartupPreflight_Connectivity(t *testing.T) {
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	r := &fakePreflightRunner{revision: "deadbeef", doctorExit: 3}

	h := startupPreflight(context.Background(), r, em)
	if h.Class != daemon.HaltPreflight {
		t.Fatalf("startupPreflight().Class = %v, want %v (a refusal for doctor exit 3)", h.Class, daemon.HaltPreflight)
	}
}

// TestStartupPreflight_RunsExactlyOnce asserts one RunDoctor call per
// startupPreflight; once per startup rests on mainRun's call site.
func TestStartupPreflight_RunsExactlyOnce(t *testing.T) {
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
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
	em := newTestEmitter(&buf)
	r := &fakePreflightRunner{fetchErr: errors.New("git fetch boom")}

	h := startupPreflight(context.Background(), r, em)
	if h.Class != daemon.HaltPreflight {
		t.Fatalf("startupPreflight().Class = %v, want %v (a refusal on a resolve failure)", h.Class, daemon.HaltPreflight)
	}
	if r.calls() != 0 {
		t.Errorf("RunDoctor called %d times, want 0 (never reached)", r.calls())
	}

	// The matching "halt" event is finish's, not exercised here.
	events := decodePreflightEvents(t, &buf)
	if len(events) != 1 {
		t.Fatalf("events = %+v, want exactly one preflight error event", events)
	}
	if events[0].Event != "preflight" || events[0].Outcome != "doctor-seam-error" {
		t.Errorf("events[0] = %+v, want preflight/doctor-seam-error", events[0])
	}
}

// TestStartupPreflight_FeatureBranchGone pins that a gone --feature-branch
// refuses startup as HaltPreflight (HaltFeatureBranchGone is Loop's class),
// naming branch and remote, without RunDoctor, under its own outcome label
// (issue #3883).
func TestStartupPreflight_FeatureBranchGone(t *testing.T) {
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	r := &fakePreflightRunner{fetchErr: &daemon.FeatureBranchGoneError{Remote: "origin", Branch: "feature-x"}}

	h := startupPreflight(context.Background(), r, em)
	if h.Class != daemon.HaltPreflight {
		t.Fatalf("startupPreflight().Class = %v, want %v", h.Class, daemon.HaltPreflight)
	}
	if !strings.Contains(h.String(), "feature-x") || !strings.Contains(h.String(), "origin") {
		t.Errorf("startupPreflight().String() = %q, want it to name the branch and the remote", h.String())
	}
	if r.calls() != 0 {
		t.Errorf("RunDoctor called %d times, want 0 (never reached)", r.calls())
	}

	events := decodePreflightEvents(t, &buf)
	if len(events) != 1 {
		t.Fatalf("events = %+v, want exactly one preflight event", events)
	}
	if events[0].Event != "preflight" || events[0].Outcome != "doctor-feature-branch-gone" {
		t.Errorf("events[0] = %+v, want preflight/doctor-feature-branch-gone", events[0])
	}
}

// TestStartupPreflight_NeverEvaluatesSelfPath pins issue #3625:
// startupPreflight must never reach ResolveTip's self `nix eval`, whose
// failure hard-refused startup. It drives a real *hostRunner, since only the
// real ResolveTip has an eval half to wrongly reach; the eval stub fails, and
// the assertion is zero calls, so a quietly passing stub cannot hide it.
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
	em := newTestEmitter(&buf)
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
	em := newTestEmitter(&buf)
	r := &fakePreflightRunner{revision: "deadbeef", doctorErr: errors.New("exec boom")}

	h := startupPreflight(context.Background(), r, em)
	if h.Class != daemon.HaltPreflight {
		t.Fatalf("startupPreflight().Class = %v, want %v (a refusal on a RunDoctor seam failure)", h.Class, daemon.HaltPreflight)
	}
}

// TestStartupPreflight_RunDoctorSeamErrorCarriesNoDaemonPrefix pins that a
// real RunDoctor seam error (the doctor binary missing) comes back with no
// "daemon: " of its own — mainRun's preflight-halt print adds the only one.
func TestStartupPreflight_RunDoctorSeamErrorCarriesNoDaemonPrefix(t *testing.T) {
	origFetch, origDoctor := runnerFetchCommand, runnerDoctorCommand
	t.Cleanup(func() { runnerFetchCommand, runnerDoctorCommand = origFetch, origDoctor })

	runnerFetchCommand = scriptedFetchSeamT(t, func() string { return "deadbeef" }, nil)
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/nonexistent/spindrift-doctor")
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})

	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	got := startupPreflight(context.Background(), r, em).String()
	if !strings.HasPrefix(got, "preflight: run-doctor: ") {
		t.Errorf("startupPreflight().String() = %q, want prefix %q", got, "preflight: run-doctor: ")
	}
	if n := strings.Count(got, "daemon: "); n != 0 {
		t.Errorf(`startupPreflight().String() = %q, want zero "daemon: " (got %d)`, got, n)
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
			em := newTestEmitter(&buf)
			h := startupPreflight(ctx, tt.r, em)
			if h.Class != daemon.HaltOperatorStop {
				t.Fatalf("startupPreflight().Class = %v, want %v", h.Class, daemon.HaltOperatorStop)
			}

			// A cancelled preflight still reaches the durable event stream
			// via "preflight"; the matching "halt" is finish's, not exercised here.
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
	em := newTestEmitter(&buf)
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

// TestFinish_OnePathForEveryPreLoopHalt pins finish (issue #3622): each
// pre-loop halt emits one "halt" event, prints its Diagnostic and returns
// its class's exit code; the instance-lock refusal (sw == nil) leaves no
// status file.
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
		em := newTestEmitter(&buf)
		r := &fakePreflightRunner{revision: "deadbeef", doctorExit: 4}
		h := startupPreflight(context.Background(), r, em)

		dir := t.TempDir()
		sw := daemon.NewStatusWriter(dir, func() time.Time { return time.Unix(0, 0).UTC() })
		if got := finish(&stderr, em, sw, []daemon.Kind{daemon.KindOf(dispatchkind.Work)}, h); got != daemon.ExitPreflightFailed {
			t.Errorf("finish() = %d, want %d", got, daemon.ExitPreflightFailed)
		}
		if got := countHalts(t, &buf, h); got != 1 {
			t.Errorf("halt events = %d, want exactly 1", got)
		}
		// The tail is doctor's remedy prose, owned elsewhere: pin the prefix
		// literally and the rest against the event's own reason.
		if prefix := "daemon: preflight: doctor exit 4: required labels are missing — remedy: "; !strings.HasPrefix(stderr.String(), prefix) {
			t.Errorf("stderr = %q, want prefix %q", stderr.String(), prefix)
		}
		if want := "daemon: " + h.String() + "\n"; stderr.String() != want {
			t.Errorf("stderr = %q, want %q", stderr.String(), want)
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
		em := newTestEmitter(&buf)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		h := startupPreflight(ctx, &fakePreflightRunner{}, em)

		dir := t.TempDir()
		sw := daemon.NewStatusWriter(dir, func() time.Time { return time.Unix(0, 0).UTC() })
		if got := finish(&stderr, em, sw, []daemon.Kind{daemon.KindOf(dispatchkind.Work)}, h); got != 0 {
			t.Errorf("finish() = %d, want 0", got)
		}
		if got := countHalts(t, &buf, h); got != 1 {
			t.Errorf("halt events = %d, want exactly 1", got)
		}
		if want := "daemon: context-cancelled: context canceled\n"; stderr.String() != want {
			t.Errorf("stderr = %q, want %q", stderr.String(), want)
		}
	})

	t.Run("instance lock refusal", func(t *testing.T) {
		var buf, stderr bytes.Buffer
		em := newTestEmitter(&buf)
		h := daemon.Halt{Class: daemon.HaltInstanceLock, Detail: "held by pid 123"}

		dir := t.TempDir()
		if got := finish(&stderr, em, nil, []daemon.Kind{daemon.KindOf(dispatchkind.Work)}, h); got != 1 {
			t.Errorf("finish() = %d, want 1", got)
		}
		if got := countHalts(t, &buf, h); got != 1 {
			t.Errorf("halt events = %d, want exactly 1", got)
		}
		if want := "daemon: held by pid 123\n"; stderr.String() != want {
			t.Errorf("stderr = %q, want %q", stderr.String(), want)
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

// TestStrippedKeys pins strippedKeys' sorted-order contract and its nil
// handling: a nil doc and a doc with a nil Settings map must both come back
// as an empty slice rather than panicking (JSON with no "settings" key
// unmarshals to a nil map, not an empty one).
func TestStrippedKeys(t *testing.T) {
	if got := strippedKeys(nil); len(got) != 0 {
		t.Errorf("strippedKeys(nil) = %v, want empty", got)
	}
	if got := strippedKeys(&inputdoc.Document{}); len(got) != 0 {
		t.Errorf("strippedKeys(&inputdoc.Document{}) = %v, want empty", got)
	}
	doc := &inputdoc.Document{Settings: map[string]string{
		"MODEL":        "opus",
		"BASE_BRANCH":  "main",
		"MAX_PARALLEL": "1",
	}}
	got := strippedKeys(doc)
	want := []string{"BASE_BRANCH", "MAX_PARALLEL", "MODEL"}
	if !slices.Equal(got, want) {
		t.Errorf("strippedKeys(doc) = %v, want %v (sorted)", got, want)
	}
}

// A key whose document value does not count is not stripped from the children's
// env, so it is not in the strip list; an empty value for a knob whose empty
// value is itself a setting still is.
func TestStrippedKeys_OmitsKeysWhoseValueDoesNotCount(t *testing.T) {
	doc := &inputdoc.Document{Settings: map[string]string{
		"REPO_SLUG":           "",
		"BASE_BRANCH":         "",
		"MODEL":               "x",
		"MEMORY_LIMIT":        "",
		"CONTINUOUS_DISPATCH": "",
	}}
	got := strippedKeys(doc)
	want := []string{"CONTINUOUS_DISPATCH", "MEMORY_LIMIT", "MODEL"}
	if !slices.Equal(got, want) {
		t.Errorf("strippedKeys(doc) = %v, want %v", got, want)
	}
}

// An ambient knob the document carries only as an empty value is not warned
// about: the child still inherits it, so it is not "not forwarded".
func TestMainRun_StrippedEnvWarningSkipsKeyWhoseDocumentValueDoesNotCount(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	clearKnobEnvT(t)
	t.Setenv("MODEL", "opus")
	t.Setenv("REPO_SLUG", "o/r")
	doc := validKnobDocument()
	doc.Settings["MODEL"] = "x"
	doc.Settings["REPO_SLUG"] = ""
	path := writeInputDocument(t, doc)
	t.Chdir(t.TempDir())

	var stdout, stderr bytes.Buffer
	mainRun([]string{"--input", path, "dispatch"}, &stdout, &stderr)
	out := stderr.String()
	if !strings.Contains(out, "MODEL=opus set in environment — ignored by the daemon and its children") {
		t.Errorf("stderr = %q, want the MODEL stripped-env warning", out)
	}
	if strings.Contains(out, "REPO_SLUG=") {
		t.Errorf("stderr = %q, want no REPO_SLUG warning", out)
	}
}

// TestWarnStrippedChildEnv_SetKnobWarnsExactlyOnce asserts an exported knob
// present in keys produces exactly one "ignored by the daemon and its children" line
// naming it — not zero, not a duplicate.
func TestWarnStrippedChildEnv_SetKnobWarnsExactlyOnce(t *testing.T) {
	t.Setenv("MODEL", "opus")
	var stderr bytes.Buffer
	warnStrippedChildEnv([]string{"MODEL"}, &stderr)
	got := strings.Count(stderr.String(), "ignored by the daemon and its children")
	if got != 1 {
		t.Errorf("warning count = %d, want 1; stderr=%q", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "MODEL=opus") {
		t.Errorf("stderr = %q, want it to name MODEL=opus", stderr.String())
	}
}

// TestWarnStrippedChildEnv_DaemonOnlyKnobKeepsNotForwardedWording asserts a
// daemon-only knob is still honoured from the ambient env by
// inputdoc.Document.Lookup, so its line must not claim the daemon ignores it.
func TestWarnStrippedChildEnv_DaemonOnlyKnobKeepsNotForwardedWording(t *testing.T) {
	t.Setenv("DAEMON_IDLE_FLOOR", "5s")
	var stderr bytes.Buffer
	warnStrippedChildEnv([]string{"DAEMON_IDLE_FLOOR"}, &stderr)
	want := "DAEMON_IDLE_FLOOR=5s set in environment — not forwarded to children; use the --input document's settings.DAEMON_IDLE_FLOOR\n"
	if stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}

// TestWarnStrippedChildEnv_UnsetKnobWarnsNever: t.Setenv registers the
// restore, then os.Unsetenv makes the key truly absent.
func TestWarnStrippedChildEnv_UnsetKnobWarnsNever(t *testing.T) {
	t.Setenv("MODEL", "x")
	os.Unsetenv("MODEL")
	var stderr bytes.Buffer
	warnStrippedChildEnv([]string{"MODEL"}, &stderr)
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty (MODEL not set)", stderr.String())
	}
}

// TestWarnStrippedChildEnv_EmptyExportWarnsNever: an exported-but-empty knob
// is not "set" for inputdoc.Document.Lookup, so not here either.
func TestWarnStrippedChildEnv_EmptyExportWarnsNever(t *testing.T) {
	t.Setenv("ISSUE_NUMBER", "")
	var stderr bytes.Buffer
	warnStrippedChildEnv([]string{"ISSUE_NUMBER"}, &stderr)
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty (ISSUE_NUMBER exported empty)", stderr.String())
	}
}

// TestWarnStrippedChildEnv_MultipleKeysSortedOrder asserts multiple set
// knobs are warned about in the order keys arrives in (strippedKeys already
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

// TestMainRun_StrippedEnvWarningPrecedesPreflight asserts the stripped-knob
// warning precedes repoRoot's "not a git checkout", which itself precedes
// startupPreflight; the real preflight needs nix.
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
	iWarn := strings.Index(out, "MODEL=opus set in environment — ignored by the daemon and its children")
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
	if strings.Contains(stderr.String(), "set in environment") {
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

// assertCapturedEnvT asserts env inherited PATH and HOME and lost the
// settings key MODEL. Failures name keys only: env is the live environment,
// and printing it would leak real credentials into the test log.
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

// capturedEnvFixtureT is TestMainRun_*ChildGetsCapturedEnv's setup: a
// synthetic HOME, a bare-origin consumer repo as cwd, and a document carrying
// MODEL plus extra. It returns the document path.
func capturedEnvFixtureT(t *testing.T, extra map[string]string) string {
	return capturedEnvFixtureArtifactsT(t, extra, inputdoc.Artifacts{})
}

// capturedEnvFixtureArtifactsT is capturedEnvFixtureT with extra Artifacts
// merged into the written document.
func capturedEnvFixtureArtifactsT(t *testing.T, extra map[string]string, artifacts inputdoc.Artifacts) string {
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
	doc.Artifacts = artifacts
	for k, v := range extra {
		doc.Settings[k] = v
	}
	path := writeInputDocument(t, doc)

	t.Chdir(dirConsumer)
	return path
}

// TestMainRun_FeatureBranchPrintsStartupLineAndReachesDoctor pins issue
// #3882: --feature-branch reaches doctor as --base-branch and prints the
// startup line naming both branches; unset, both are absent.
func TestMainRun_FeatureBranchPrintsStartupLineAndReachesDoctor(t *testing.T) {
	tests := []struct {
		name              string
		args              []string
		wantLine          string
		wantArgvHas       string
		wantNoLine        bool
		wantArgvEmpty     bool
		pushFeatureBranch bool
	}{
		{
			name:              "feature branch set",
			args:              []string{"--feature-branch", "feature-x", "dispatch"},
			wantLine:          "daemon: tracking main; children target feature-x",
			wantArgvHas:       "--base-branch feature-x",
			pushFeatureBranch: true,
		},
		{
			name:          "feature branch unset",
			args:          []string{"dispatch"},
			wantNoLine:    true,
			wantArgvEmpty: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := capturedEnvFixtureT(t, nil)
			if tt.pushFeatureBranch {
				// fetchRevision requires the branch on origin (issue #3883).
				gitRunT(t, "", "checkout", "-b", "feature-x")
				gitRunT(t, "", "push", "-u", "origin", "feature-x")
				gitRunT(t, "", "checkout", "main")
			}

			origDoctor := runnerDoctorCommand
			t.Cleanup(func() { runnerDoctorCommand = origDoctor })
			var gotArgv []string
			runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
				gotArgv = append([]string{name}, args...)
				return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1")
			}

			var stdout, stderr bytes.Buffer
			got := mainRun(append([]string{"--input", path}, tt.args...), &stdout, &stderr)
			if got != daemon.ExitPreflightFailed {
				t.Fatalf("mainRun() = %d, want %d (daemon.ExitPreflightFailed); stderr = %q", got, daemon.ExitPreflightFailed, stderr.String())
			}

			joined := strings.Join(gotArgv, " ")
			if tt.wantNoLine {
				if strings.Contains(stderr.String(), "daemon: tracking") {
					t.Errorf("stderr = %q, want no startup line (no --feature-branch)", stderr.String())
				}
			} else if !strings.Contains(stderr.String(), tt.wantLine) {
				t.Errorf("stderr = %q, want the startup line naming both branches", stderr.String())
			}
			if tt.wantArgvEmpty {
				if strings.Contains(joined, "--base-branch") {
					t.Errorf("doctor argv %v carries --base-branch, want none", gotArgv)
				}
			} else if !strings.Contains(joined, tt.wantArgvHas) {
				t.Errorf("doctor argv %v does not carry %q", gotArgv, tt.wantArgvHas)
			}
		})
	}
}

// TestMainRun_DocumentBaseBranchBeatsAmbient pins issue #4623: a document
// BASE_BRANCH is stripped from the child's env, so the child reads the
// document alone; the daemon must track the same branch, not an ambient
// override the child never sees.
func TestMainRun_DocumentBaseBranchBeatsAmbient(t *testing.T) {
	path := capturedEnvFixtureT(t, nil)
	t.Setenv("BASE_BRANCH", "dev")
	gitRunT(t, "", "checkout", "-b", "feature-x")
	gitRunT(t, "", "push", "-u", "origin", "feature-x")
	gitRunT(t, "", "checkout", "main")

	origDoctor := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = origDoctor })
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1")
	}

	var stdout, stderr bytes.Buffer
	mainRun([]string{"--input", path, "--feature-branch", "feature-x", "dispatch"}, &stdout, &stderr)
	want := "daemon: tracking main; children target feature-x"
	if !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %q, want it to contain %q (document BASE_BRANCH, not ambient dev)", stderr.String(), want)
	}
}

// TestMainRun_ButlerInPlayReachesDoctorAsButlerFlag pins issue #3920: a
// butler that survives gateKinds must reach the doctor preflight's
// argv as --butler, whichever verb form put it in play, and a gated-out or
// never-selected butler must not.
func TestMainRun_ButlerInPlayReachesDoctorAsButlerFlag(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		overrides  map[string]string
		wantButler bool
	}{
		{
			name:       "bare daemon with chores enabled carries --butler",
			args:       nil,
			overrides:  map[string]string{"BUTLER_CHORES": "bugs", "RESEARCH_RESERVATION": "0"},
			wantButler: true,
		},
		{
			name:       "bare daemon with no chores drops --butler",
			args:       nil,
			overrides:  map[string]string{"RESEARCH_RESERVATION": "0"},
			wantButler: false,
		},
		{
			name:       "explicit dispatch selector never carries --butler",
			args:       []string{"dispatch"},
			overrides:  map[string]string{"BUTLER_CHORES": "bugs"},
			wantButler: false,
		},
		{
			name:       "explicit butler selector carries --butler",
			args:       []string{"butler"},
			overrides:  map[string]string{"BUTLER_CHORES": "bugs"},
			wantButler: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := capturedEnvFixtureT(t, tt.overrides)

			origDoctor := runnerDoctorCommand
			t.Cleanup(func() { runnerDoctorCommand = origDoctor })
			var gotArgv []string
			runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
				gotArgv = append([]string{name}, args...)
				return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1")
			}

			var stdout, stderr bytes.Buffer
			got := mainRun(append([]string{"--input", path}, tt.args...), &stdout, &stderr)
			if got != daemon.ExitPreflightFailed {
				t.Fatalf("mainRun() = %d, want %d (daemon.ExitPreflightFailed); stderr = %q", got, daemon.ExitPreflightFailed, stderr.String())
			}

			hasButler := slices.Contains(gotArgv, "--butler")
			if hasButler != tt.wantButler {
				t.Errorf("doctor argv %v has --butler = %v, want %v", gotArgv, hasButler, tt.wantButler)
			}
		})
	}
}

// TestMainRun_DoctorChildGetsCapturedEnv pins the bug where RunDoctor exec'd
// doctor with an empty environment and dogfood's preflight could not find
// `sh`. The stubbed doctor fails, so mainRun halts before daemon.Loop.
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
// TestMainRun_DoctorChildGetsCapturedEnv. The child exits 1 (unclassified)
// and DAEMON_BREAKER_THRESHOLD=1 trips the breaker on it, so Loop halts at
// once instead of backing off forever.
func TestMainRun_DispatchChildGetsCapturedEnv(t *testing.T) {
	path := capturedEnvFixtureT(t, map[string]string{"DAEMON_BREAKER_THRESHOLD": "1"})

	origDoctor := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = origDoctor })
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}

	origExec := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = origExec })
	// No mutex: Loop joins every slot goroutine before mainRun returns, and
	// a mutex could not cover cmd.Env, which runner.go sets after this returns.
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

// childDrainThenEscalate writes a distinct word to its transcript ($1) per
// signal so the test tells drain from escalation, and exits 7 (operator
// stop) on the second. `: >"$0"` arms the marker once the traps are set.
const childDrainThenEscalate = `
n=0
trap 'n=$((n+1)); if [ "$n" -eq 1 ]; then echo drain >>"$1"; else echo escalate >>"$1"; exit 7; fi' TERM INT
: >"$0"
while :; do sleep 0.05; done`

// waitForFileContains polls path until it contains want, within 2s.
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

// TestMainRun_EndToEndSignalDrainsThenEscalates is the one test wiring the
// whole drain/escalate chain (relay, announceStop, Config.Stop/Abort,
// ChildRequest, forwardSignals) to a real child receiving SIGTERM then SIGINT
// (issue #3626): a broken link fails it even if every narrower test passes.
// The latch is driven through installStopSignal, never a real signal to the
// test binary.
func TestMainRun_EndToEndSignalDrainsThenEscalates(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}
	// Also disables the self-change check (SPINDRIFT_DAEMON_PROGRAM unset):
	// this configuration then never reaches runnerEvalCommand, so there is
	// nothing else to stub.
	clearKnobEnvT(t)

	dirConsumer := bareOriginConsumerT(t)

	doc := validKnobDocument()
	// Tiny, in case a future flow adds an iteration before shutdown.
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

	// Not waitForArmed: mainRun could exit first (e.g. a failed fetch), and
	// a plain armed-file poll would then time out naming the wrong cause.
	func() {
		for i := 0; i < 1000; i++ {
			if _, err := os.Stat(armed); err == nil {
				return
			}
			select {
			case got := <-doneCh:
				t.Fatalf("mainRun returned %d before the child armed; stderr=%s", got, stderr.String())
			case <-time.After(2 * time.Millisecond):
			}
		}
		t.Fatalf("child never started and armed its signal trap: %v", armed)
	}()
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
	// The pool's Stop watcher and the slot reading exit 7 race to record the
	// halt; either is a clean stop, and only the winner's reason survives.
	if haltReason != "context-cancelled: stop requested" && haltReason != "outcome: signalled-stop" {
		t.Errorf("halt reason = %q, want %q or %q", haltReason, "context-cancelled: stop requested", "outcome: signalled-stop")
	}
}

// holdCheckoutLockT takes the checkout lock for the Work kind and holds it
// until the test ends, standing in for "another daemon already running
// against this checkout".
func holdCheckoutLockT(t *testing.T, gitDirPath string) {
	t.Helper()
	lock, err := daemon.AcquireCheckoutLock(gitDirPath, []daemon.Kind{daemon.KindOf(dispatchkind.Work)})
	if err != nil {
		t.Fatalf("AcquireCheckoutLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })
}

// lockedBuffer stands in for a merged stdout+stderr terminal; mutex-guarded
// because announceStop's goroutine can emit concurrently with mainRun.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// mergedEvent decodes line as a daemon event; ok is false for any other line
// of a merged stdout+stderr stream, such as a "daemon: ..." diagnostic.
func mergedEvent(line string) (ev daemon.Event, ok bool) {
	if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &ev) != nil {
		return daemon.Event{}, false
	}
	return ev, true
}

// assertHaltBeforeDiagnostic asserts the shape an operator's terminal sees
// when stdout and stderr share one stream: a "halt" event line followed,
// later, by the "daemon: <stderrLine>" diagnostic (issue #3711). It returns
// the lines after the diagnostic so a caller can pin what may trail it.
func assertHaltBeforeDiagnostic(t *testing.T, merged, stderrLine string) []string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(merged), "\n")
	haltAt, diagAt := -1, -1
	for i, line := range lines {
		if ev, ok := mergedEvent(line); ok && ev.Event == "halt" {
			if haltAt >= 0 {
				t.Fatalf("merged output = %q, want exactly one halt event, got a duplicate halt", merged)
			}
			haltAt = i
		} else if line == "daemon: "+stderrLine {
			diagAt = i
		}
	}
	if haltAt < 0 || diagAt < 0 {
		t.Fatalf("merged output = %q, want a halt event line and %q", merged, "daemon: "+stderrLine)
	}
	if haltAt > diagAt {
		t.Errorf("merged output = %q, want the halt event before %q", merged, "daemon: "+stderrLine)
	}
	return lines[diagAt+1:]
}

// TestMainRun_HaltEventPrecedesStderrDiagnostic pins issue #3711: every
// pre-loop refusal emits its halt event before the "daemon: <reason>" stderr
// line, so a merged terminal reads event then diagnostic.
func TestMainRun_HaltEventPrecedesStderrDiagnostic(t *testing.T) {
	failingDoctor := func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1")
	}
	missingDoctor := func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/nonexistent/spindrift-doctor")
	}
	// exec, so the kill lands on sleep itself rather than orphaning it.
	slowDoctor := func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exec sleep 30")
	}
	tests := []struct {
		name     string
		args     []string
		doctor   func(ctx context.Context, name string, args ...string) *exec.Cmd
		holdLock bool
		// stopped fires the stop latch before mainRun starts, so the real
		// installStopSignal -> announceStop -> cancelPreflight wiring
		// cancels the slow doctor.
		stopped  bool
		wantCode int
		// wantOutcome is the preflight event's outcome; "" means no
		// preflight event is expected (the lock refuses before preflight).
		wantOutcome string
	}{
		{name: "doctor exit", args: []string{"dispatch"}, doctor: failingDoctor, wantCode: daemon.ExitPreflightFailed, wantOutcome: "doctor-unclassified"},
		{name: "doctor seam error", args: []string{"dispatch"}, doctor: missingDoctor, wantCode: daemon.ExitPreflightFailed, wantOutcome: "doctor-seam-error"},
		{name: "feature branch gone", args: []string{"--feature-branch", "no-such-branch", "dispatch"}, doctor: failingDoctor, wantCode: daemon.ExitPreflightFailed, wantOutcome: "doctor-feature-branch-gone"},
		{name: "instance lock", args: []string{"dispatch"}, doctor: failingDoctor, holdLock: true, wantCode: 1},
		{name: "operator stop", args: []string{"dispatch"}, doctor: slowDoctor, stopped: true, wantCode: 0, wantOutcome: "doctor-cancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := capturedEnvFixtureT(t, nil)
			origDoctor := runnerDoctorCommand
			t.Cleanup(func() { runnerDoctorCommand = origDoctor })
			runnerDoctorCommand = tt.doctor

			if tt.holdLock {
				wd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				gitDirPath, err := gitDir(wd)
				if err != nil {
					t.Fatalf("gitDir: %v", err)
				}
				holdCheckoutLockT(t, gitDirPath)
			}
			if tt.stopped {
				stop, abort := make(chan struct{}), make(chan struct{})
				close(stop)
				origInstall := installStopSignal
				t.Cleanup(func() { installStopSignal = origInstall })
				installStopSignal = func() (<-chan struct{}, <-chan struct{}, func()) {
					return stop, abort, func() {}
				}
			}

			var merged lockedBuffer
			got := mainRun(append([]string{"--input", path}, tt.args...), &merged, &merged)
			if got != tt.wantCode {
				t.Fatalf("mainRun() = %d, want %d; output = %q", got, tt.wantCode, merged.String())
			}

			preflightAt, haltAt, haltReason, outcome := -1, -1, "", ""
			for i, line := range strings.Split(merged.String(), "\n") {
				ev, ok := mergedEvent(line)
				if !ok {
					continue
				}
				switch ev.Event {
				case "preflight":
					preflightAt, outcome = i, ev.Outcome
				case "halt":
					haltAt, haltReason = i, ev.Reason
				}
			}
			if outcome != tt.wantOutcome {
				t.Errorf("preflight outcome = %q, want %q; output = %q", outcome, tt.wantOutcome, merged.String())
			}
			if tt.wantOutcome != "" && preflightAt > haltAt {
				t.Errorf("merged output = %q, want the preflight event before the halt event", merged.String())
			}
			// The lock diagnostic is the bare error, without the halt
			// reason's class prefix; the others print the reason itself.
			diag := haltReason
			if tt.holdLock {
				prefix := daemon.Halt{Class: daemon.HaltInstanceLock}.String()
				if !strings.HasPrefix(haltReason, prefix) {
					t.Fatalf("halt reason = %q, want prefix %q", haltReason, prefix)
				}
				diag = strings.TrimPrefix(haltReason, prefix)
			}
			if trailing := assertHaltBeforeDiagnostic(t, merged.String(), diag); strings.TrimSpace(strings.Join(trailing, "")) != "" {
				t.Errorf("trailing output after diagnostic = %q, want none on a writable status dir", trailing)
			}
		})
	}
}

// TestFinish_StatusWriteWarningTrailsDiagnostic pins issue #3711's other
// half: a failed status write's warning trails the halt event and
// diagnostic. It is checked at finish, since mainRun's status file lives in
// the git dir, which cannot be made unwritable portably.
func TestFinish_StatusWriteWarningTrailsDiagnostic(t *testing.T) {
	kinds := []daemon.Kind{daemon.KindOf(dispatchkind.Work)}
	for _, tt := range []struct {
		name        string
		statusDir   func(t *testing.T) string
		wantWarning bool
	}{
		{name: "status written", statusDir: func(t *testing.T) string { return t.TempDir() }},
		{name: "status write fails", statusDir: func(t *testing.T) string {
			// A path under a regular file can never be created.
			file := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(file, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(file, "dir")
		}, wantWarning: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var merged lockedBuffer
			clock := func() time.Time { return time.Unix(0, 0).UTC() }
			em := daemon.NewEmitter(&merged, &merged, clock)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			h := startupPreflight(ctx, &fakePreflightRunner{}, em)
			sw := daemon.NewStatusWriter(tt.statusDir(t), clock)

			if got := finish(&merged, em, sw, kinds, h); got != 0 {
				t.Errorf("finish() = %d, want 0 for an operator cancel", got)
			}

			trailing := assertHaltBeforeDiagnostic(t, merged.String(), h.String())
			if tt.wantWarning {
				if len(trailing) != 1 || !strings.HasPrefix(trailing[0], "daemon: status file write failed: ") {
					t.Errorf("trailing = %q, want exactly the status-write warning", trailing)
				}
			} else if strings.TrimSpace(strings.Join(trailing, "")) != "" {
				t.Errorf("trailing = %q, want none", trailing)
			}
		})
	}
}

// TestInvalidConfigHaltParity pins issue #5207: daemon.Loop's config-invalid
// rejection and finish both publish the halt through the one shared
// Emitter.PublishHalted, so for the same Halt they emit the same halt event,
// write the same halted status, and warn identically when the status write
// fails. finish's stderr diagnostic is its alone and is not compared.
func TestInvalidConfigHaltParity(t *testing.T) {
	kinds := []daemon.Kind{daemon.KindOf(dispatchkind.Work)}
	clock := func() time.Time { return time.Unix(0, 0).UTC() }

	tmpSuffix := regexp.MustCompile(`tmp-\d+`)
	type outcome struct {
		events, warn string
		status       *daemon.Status
	}
	// run gives each path its own Emitter and StatusWriter; dir is fresh per
	// path so a finish that skipped its write could not pass on Loop's file.
	run := func(dir string, halt func(em *daemon.Emitter, sw *daemon.StatusWriter) daemon.Halt) (outcome, daemon.Halt) {
		var out, errW bytes.Buffer
		em := daemon.NewEmitter(&out, &errW, clock)
		h := halt(em, daemon.NewStatusWriter(dir, clock))
		// The temp file name carries a random suffix.
		o := outcome{events: out.String(), warn: tmpSuffix.ReplaceAllString(errW.String(), "tmp-N")}
		if report, err := daemon.ReadStatus(dir); err == nil {
			o.status = report.Status
		}
		return o, h
	}

	for _, tt := range []struct {
		name        string
		statusDirs  func(t *testing.T) [2]string
		wantWarning bool
	}{
		{name: "status written", statusDirs: func(t *testing.T) [2]string {
			return [2]string{t.TempDir(), t.TempDir()}
		}},
		{name: "status write fails", statusDirs: func(t *testing.T) [2]string {
			// A path under a regular file can never be created. Both paths
			// share it because the warning names the path.
			file := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(file, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(file, "dir")
			return [2]string{dir, dir}
		}, wantWarning: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dirs := tt.statusDirs(t)

			// Slots <= 0 is rejected before any pool or Runner is touched.
			viaLoop, h := run(dirs[0], func(em *daemon.Emitter, sw *daemon.StatusWriter) daemon.Halt {
				return daemon.Loop(context.Background(), daemon.Config{Kinds: kinds, Status: sw}, nil, em, hostClock{})
			})
			if h.Class != daemon.HaltInvalidConfig {
				t.Fatalf("Loop halt class = %v, want HaltInvalidConfig", h.Class)
			}
			viaFinish, _ := run(dirs[1], func(em *daemon.Emitter, sw *daemon.StatusWriter) daemon.Halt {
				finish(io.Discard, em, sw, kinds, h)
				return h
			})

			if viaLoop.events != viaFinish.events {
				t.Errorf("halt events differ:\nLoop:   %q\nfinish: %q", viaLoop.events, viaFinish.events)
			}
			if viaLoop.warn != viaFinish.warn {
				t.Errorf("warnings differ:\nLoop:   %q\nfinish: %q", viaLoop.warn, viaFinish.warn)
			}
			if tt.wantWarning {
				if !strings.HasPrefix(viaLoop.warn, "daemon: status file write failed: ") {
					t.Errorf("warning = %q, want the status-write warning", viaLoop.warn)
				}
				return
			}
			if viaLoop.warn != "" {
				t.Errorf("Loop warning = %q, want none", viaLoop.warn)
			}
			if viaFinish.warn != "" {
				t.Errorf("finish warning = %q, want none", viaFinish.warn)
			}
			if viaLoop.status == nil || viaFinish.status == nil {
				t.Fatalf("status missing: Loop %v, finish %v", viaLoop.status, viaFinish.status)
			}
			l, f := viaLoop.status, viaFinish.status
			if l.State != f.State || l.Reason != f.Reason || !slices.Equal(l.Kinds, f.Kinds) {
				t.Errorf("status differs:\nLoop:   %+v\nfinish: %+v", l, f)
			}
		})
	}
}

// TestStartupPreflight_ExitFourNamesMissingLabels pins that doctor's own
// labels line, not ClassifyPreflight's generic text, becomes the halt's
// detail: exit 4 also covers the butler/research/Filer label sets.
func TestStartupPreflight_ExitFourNamesMissingLabels(t *testing.T) {
	var buf bytes.Buffer
	em := newTestEmitter(&buf)
	line := doctor.ErrRequiredLabelsMissing.Error() + ": agent-butler-finding missing — create them in the repository"
	r := &fakePreflightRunner{revision: "deadbeef", doctorExit: 4, doctorLine: line}

	h := startupPreflight(context.Background(), r, em)
	if h.Class != daemon.HaltPreflight {
		t.Fatalf("startupPreflight().Class = %v, want %v", h.Class, daemon.HaltPreflight)
	}
	if !strings.Contains(h.String(), "agent-butler-finding") || !strings.Contains(h.Detail, line) {
		t.Errorf("halt = %q (detail %q), want it to name agent-butler-finding", h.String(), h.Detail)
	}
	if !strings.Contains(buf.String(), `"outcome":"doctor-required-labels-missing"`) {
		t.Errorf("events = %q, want outcome doctor-required-labels-missing unchanged", buf.String())
	}
}

// TestDoctorPreflightFlags pins which kind flags the preflight gets, per each
// kind's Preflight row: a kept butler always carries --butler, but research
// carries --research only when named, since the bare every-kind default lists
// it and a dispatch-only repo without the research labels must still start.
func TestDoctorPreflightFlags(t *testing.T) {
	for _, tc := range []struct {
		name   string
		argv   []string
		chores string
		want   []string
	}{
		{"bare without chores", nil, "", nil},
		{"dispatch", []string{"dispatch"}, "bugs", nil},
		{"research", []string{"research"}, "", []string{"--research"}},
		{"bare with chores", nil, "bugs", []string{"--butler"}},
		{"butler", []string{"butler"}, "bugs", []string{"--butler"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args, err := parseArgs(append([]string{"--input", "x.json"}, tc.argv...))
			if err != nil {
				t.Fatalf("parseArgs: %v", err)
			}
			args.Kinds, err = gateKinds(args.Kinds, args.ExplicitSelector, gateKnobs{chores: chore.Knobs{Chores: tc.chores}})
			if err != nil {
				t.Fatalf("gateKinds: %v", err)
			}
			got, err := doctorPreflightFlags(args.Kinds, args.ExplicitSelector)
			if err != nil {
				t.Fatalf("doctorPreflightFlags: %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("doctorPreflightFlags = %v, want %v (kinds %v, explicit %v)", got, tc.want, args.Kinds, args.ExplicitSelector)
			}
		})
	}
}

func TestSuccessorArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"wrapper input then operator args", []string{"--input", "/old/doc.json", "--feature-branch", "feat", "research"}, []string{"--feature-branch", "feat", "research"}},
		{"no input", []string{"dispatch"}, []string{"dispatch"}},
		{"input mid-argv", []string{"dispatch", "--input", "a", "--input", "b", "--check-only"}, []string{"dispatch", "--check-only"}},
		{"empty", nil, []string{}},
		// A replayed handoff fd would name a descriptor the next image never got.
		{"handoff fd", []string{"--input", "d", "--handoff-fd", "7", "dispatch", "--restart-in-place"}, []string{"dispatch", "--restart-in-place"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := successorArgs(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("successorArgs(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestResolveDriverEnv_MismatchWarnsOnceAndReturnsMarker(t *testing.T) {
	t.Setenv("DRIVER", "opencode")
	t.Setenv(inputdoc.DriverWarnedEnv, "")
	doc := &inputdoc.Document{Artifacts: claudeArtifacts()}
	var stderr bytes.Buffer
	got := resolveDriverEnv(doc, &stderr)
	if want := []string{"SPINDRIFT_DRIVER_WARNED=opencode/claude"}; !slices.Equal(got, want) {
		t.Errorf("env entries = %v, want %v", got, want)
	}
	out := stderr.String()
	if n := strings.Count(out, "using the baked Driver"); n != 1 {
		t.Errorf("warning count = %d, want 1; stderr=%q", n, out)
	}
	for _, want := range []string{"DRIVER=opencode", "baked Driver claude"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr = %q, want it to contain %q", out, want)
		}
	}
}

func TestResolveDriverEnv_MatchUnsetOrNoDocIsSilentWithNoMarker(t *testing.T) {
	baked := &inputdoc.Document{Artifacts: claudeArtifacts()}
	cases := []struct {
		name    string
		doc     *inputdoc.Document
		ambient string
		warned  string
	}{
		{"match", baked, "claude", ""},
		{"unset", baked, "", ""},
		{"no document", nil, "opencode", ""},
		{"document without DRIVER", &inputdoc.Document{}, "opencode", ""},
		{"already warned about this pair", baked, "opencode", "opencode/claude"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DRIVER", tc.ambient)
			t.Setenv(inputdoc.DriverWarnedEnv, tc.warned)
			var stderr bytes.Buffer
			if got := resolveDriverEnv(tc.doc, &stderr); len(got) != 0 {
				t.Errorf("env entries = %v, want none", got)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

// unsetEnvT makes key absent from os.Environ() for the test body: t.Setenv
// registers the restore, Unsetenv drops the key, which the child-env marker
// assertions read.
func unsetEnvT(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

// driverMarkerEnvT returns the value of the DriverWarnedEnv entry in env, and
// whether one is present.
func driverMarkerEnvT(env []string) (string, bool) {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, inputdoc.DriverWarnedEnv+"="); ok {
			return v, true
		}
	}
	return "", false
}

// TestMainRun_DriverMismatch_WarnsOnceAndMarksChildren pins issue #5050 end to
// end at the daemon: one stderr warning, and both the doctor preflight and a
// dispatch child carry the marker that survives their harness.env re-sourcing.
func TestMainRun_DriverMismatch_WarnsOnceAndMarksChildren(t *testing.T) {
	path := capturedEnvFixtureArtifactsT(t, map[string]string{"DAEMON_BREAKER_THRESHOLD": "1"}, claudeArtifacts())
	t.Setenv("DRIVER", "opencode")
	unsetEnvT(t, inputdoc.DriverWarnedEnv)

	origDoctor := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = origDoctor })
	var doctorCmd *exec.Cmd
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		doctorCmd = exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
		return doctorCmd
	}
	origExec := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = origExec })
	var childCmd *exec.Cmd
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		childCmd = exec.Command("/bin/sh", "-c", "exit 1")
		return childCmd
	}

	var stdout, stderr bytes.Buffer
	mainRun([]string{"--input", path, "dispatch"}, &stdout, &stderr)

	if n := strings.Count(stderr.String(), "using the baked Driver"); n != 1 {
		t.Errorf("warning count = %d, want 1; stderr=%q", n, stderr.String())
	}
	for label, cmd := range map[string]*exec.Cmd{"doctor": doctorCmd, "dispatch child": childCmd} {
		if cmd == nil {
			t.Errorf("%s command was never built", label)
			continue
		}
		if v, ok := driverMarkerEnvT(cmd.Env); !ok || v != "opencode/claude" {
			t.Errorf("%s env marker = %q (present=%v), want opencode", label, v, ok)
		}
	}
}

func TestMainRun_DriverMatch_NoWarningNoMarker(t *testing.T) {
	path := capturedEnvFixtureArtifactsT(t, nil, claudeArtifacts())
	t.Setenv("DRIVER", "claude")
	unsetEnvT(t, inputdoc.DriverWarnedEnv)

	origDoctor := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = origDoctor })
	var captured *exec.Cmd
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		captured = exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1")
		return captured
	}

	var stdout, stderr bytes.Buffer
	mainRun([]string{"--input", path, "dispatch"}, &stdout, &stderr)

	if strings.Contains(stderr.String(), "using the baked Driver") {
		t.Errorf("stderr = %q, want no Driver warning", stderr.String())
	}
	if captured == nil {
		t.Fatal("runnerDoctorCommand was never called")
	}
	if v, ok := driverMarkerEnvT(captured.Env); ok {
		t.Errorf("doctor env carries marker %q, want none", v)
	}
}

// selfExecCall is one runnerSelfExec invocation a test captured.
type selfExecCall struct {
	path      string
	argv, env []string
}

// inPlaceFixture is a mainRun setup in which the first tip fetch finds a newer
// daemon build, so a --restart-in-place pool prebuilds it, waits for its child
// to report, and execs it. Every external seam is stubbed: nix eval prints
// newSelf, nix build and the smoke test succeed, and the exec records its
// arguments and returns as if it had happened.
type inPlaceFixture struct {
	input, gitDir, goFile, newSelf string
	// stop is the latch installStopSignal hands the next mainRun.
	stop <-chan struct{}
	// installs counts installStopSignal calls.
	installs atomic.Int32
	// onInstall, when set, supplies the latch installStopSignal returns instead
	// of stop, and runs inside the call.
	onInstall func() <-chan struct{}
	// termFile is written by the child when it receives SIGTERM.
	termFile string
	// childCreated closes when runnerExecCommand creates the first child.
	childCreated     chan struct{}
	childCreatedOnce sync.Once

	mu       sync.Mutex
	children []*exec.Cmd
	execs    []selfExecCall
	execErr  error
}

// inPlaceOldSelf is the daemon image an in-place restart starts from.
const inPlaceOldSelf = "/nix/store/aaa-old/bin/daemon"

// inPlaceNewSelf is the daemon image an in-place restart execs into.
const inPlaceNewSelf = "/nix/store/bbb-new/bin/daemon"

// inPlaceConsumerT readies a bare-origin Consumer checkout and an input
// document with DAEMON_SELF_APP set, skipping when the restart cannot run here.
func inPlaceConsumerT(t *testing.T) (consumer, input, gd string) {
	t.Helper()
	requireInPlaceRestart(t)
	for _, bin := range []string{"git", "/bin/sh"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}
	clearKnobEnvT(t)
	consumer = bareOriginConsumerT(t)
	doc := validKnobDocument()
	doc.Settings["DAEMON_SELF_APP"] = ".#dogfood"
	input = writeInputDocument(t, doc)
	gd, err := gitDir(consumer)
	if err != nil {
		t.Fatal(err)
	}
	return consumer, input, gd
}

// newInPlaceFixture sets exactly the fields stubExternalCommands reads.
func newInPlaceFixture(goFile, termFile, newSelf string) *inPlaceFixture {
	return &inPlaceFixture{goFile: goFile, termFile: termFile, newSelf: newSelf, childCreated: make(chan struct{})}
}

func newInPlaceFixtureT(t *testing.T, newSelf string) *inPlaceFixture {
	t.Helper()
	consumer, input, gd := inPlaceConsumerT(t)
	setSignalSettle(t, 0)
	f := newInPlaceFixture(filepath.Join(t.TempDir(), "go"), filepath.Join(t.TempDir(), "term"), newSelf)
	f.input, f.gitDir, f.stop = input, gd, make(chan struct{})
	t.Chdir(consumer)
	t.Setenv("SPINDRIFT_DAEMON_PROGRAM", inPlaceOldSelf)
	f.stubExternalCommands(t)
	f.stubInProcessSeams(t)
	return f
}

// stubInProcessSeams replaces the stop handler and the exec: the two seams a
// real-exec test keeps real or replaces itself.
func (f *inPlaceFixture) stubInProcessSeams(t *testing.T) {
	t.Helper()
	origInstall, origSelfExec := installStopSignal, runnerSelfExec
	t.Cleanup(func() { installStopSignal, runnerSelfExec = origInstall, origSelfExec })
	installStopSignal = func() (<-chan struct{}, <-chan struct{}, func()) {
		f.installs.Add(1)
		if f.onInstall != nil {
			return f.onInstall(), make(chan struct{}), func() {}
		}
		return f.stop, make(chan struct{}), func() {}
	}
	runnerSelfExec = func(path string, argv, env []string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.execs = append(f.execs, selfExecCall{path, slices.Clone(argv), slices.Clone(env)})
		return f.execErr
	}
}

// stubExternalCommands stubs doctor, eval, prebuild and the child exec for the
// rest of the test.
func (f *inPlaceFixture) stubExternalCommands(t *testing.T) {
	t.Helper()
	newSelf := f.newSelf
	sh := func(ctx context.Context, script string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", script)
	}
	origDoctor, origEval, origPrebuild, origExec := runnerDoctorCommand, runnerEvalCommand, runnerPrebuildCommand, runnerExecCommand
	t.Cleanup(func() {
		runnerDoctorCommand, runnerEvalCommand, runnerPrebuildCommand, runnerExecCommand = origDoctor, origEval, origPrebuild, origExec
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, c := range f.children {
			if c.Process != nil {
				_ = c.Process.Kill()
			}
		}
	})
	runnerDoctorCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd { return sh(ctx, "exit 0") }
	runnerEvalCommand = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		if slices.Contains(args, "--apply") {
			return sh(ctx, "echo /nix/store/ccc-daemon.drv")
		}
		return sh(ctx, "echo "+newSelf)
	}
	// The nix stub emulates --out-link: a prebuild (<drv>^out) links the new
	// build's store path, a rooting links its argument. A prebuild waits for
	// the first child: one that beats the slot's first start puts the pool in
	// the waiting phase, which refuses that start and hands off with no child
	// to inherit.
	runnerPrebuildCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "nix" {
			out, target := filepath.Dir(filepath.Dir(newSelf)), args[len(args)-1]
			if strings.HasSuffix(target, "^out") {
				target = out
				select {
				case <-f.childCreated:
				case <-ctx.Done():
				}
			}
			link := args[slices.Index(args, "--out-link")+1]
			return exec.CommandContext(ctx, "/bin/sh", "-c", `ln -sfn "$1" "$2" && echo "$3"`, "sh", target, link, out)
		}
		return sh(ctx, "exit 0")
	}
	// One report so the pool's clean moment can arrive, then it runs until
	// the go-file appears or it is sent SIGTERM, which it records in $2.
	const child = `trap 'echo term >"$2"; exit 0' TERM
printf '%s\n' '{"event":"box","issue":"101","phase":"initial"}' >&3
while [ ! -e "$1" ]; do sleep 0.02; done`
	runnerExecCommand = func(string, ...string) *exec.Cmd {
		c := exec.Command("/bin/sh", "-c", child, "sh", f.goFile, f.termFile)
		f.mu.Lock()
		f.children = append(f.children, c)
		f.mu.Unlock()
		f.childCreatedOnce.Do(func() { close(f.childCreated) })
		return c
	}
}

// start runs mainRun on argv in the background under f.stop.
func (f *inPlaceFixture) start(argv []string) (code <-chan int, out *lockedBuffer) {
	out = &lockedBuffer{}
	c := make(chan int, 1)
	go func() { c <- mainRun(argv, out, out) }()
	return c, out
}

func waitCode(t *testing.T, code <-chan int, out *lockedBuffer) int {
	t.Helper()
	select {
	case got := <-code:
		return got
	case <-time.After(10 * time.Second):
		t.Fatalf("mainRun did not return; output:\n%s", out.String())
		return 0
	}
}

func eventsIn(out *lockedBuffer) (evs []daemon.Event) {
	for _, line := range strings.Split(out.String(), "\n") {
		if ev, ok := mergedEvent(line); ok {
			evs = append(evs, ev)
		}
	}
	return evs
}

func findEvent(evs []daemon.Event, name, phase string) (daemon.Event, bool) {
	for _, ev := range evs {
		if ev.Event == name && ev.Phase == phase {
			return ev, true
		}
	}
	return daemon.Event{}, false
}

func waitForEvent(t *testing.T, out *lockedBuffer, name, phase string) daemon.Event {
	t.Helper()
	for range 1000 {
		if ev, ok := findEvent(eventsIn(out), name, phase); ok {
			return ev
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %s event (phase %q); output:\n%s", name, phase, out.String())
	return daemon.Event{}
}

func assertNoHandoffIn(t *testing.T, what string, env []string) {
	t.Helper()
	for _, kv := range env {
		// Names only: a value is arbitrary ambient text, and may be a credential.
		key, _, _ := strings.Cut(kv, "=")
		if strings.Contains(strings.ToLower(key), "handoff") {
			t.Errorf("%s environment carries %s", what, key)
		}
	}
}

// handOff runs the first image to its exec and returns the captured call.
func (f *inPlaceFixture) handOff(t *testing.T) (selfExecCall, *lockedBuffer) {
	t.Helper()
	code, out := f.start([]string{"--input", f.input, "--restart-in-place", "dispatch"})
	if got := waitCode(t, code, out); got != 0 {
		t.Fatalf("first image exited %d; output:\n%s", got, out.String())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.execs) != 1 {
		t.Fatalf("exec calls = %d, want 1; output:\n%s", len(f.execs), out.String())
	}
	return f.execs[0], out
}

func assertLockHeld(t *testing.T, gitDirPath string, want bool) {
	t.Helper()
	lock, err := daemon.AcquireCheckoutLock(gitDirPath, []daemon.Kind{daemon.KindOf(dispatchkind.Work)})
	if err == nil {
		_ = lock.Release()
	}
	if held := err != nil; held != want {
		t.Fatalf("checkout lock held = %v, want %v (acquire err: %v)", held, want, err)
	}
}

// TestMainRun_RestartInPlaceHandsOffAndReattaches drives both images of an
// in-place restart in one process: the first execs (a stub) the prebuilt
// daemon with its child still running and the lock still held; the second,
// started from the exec's own argv, re-attaches that child and drains it.
func TestMainRun_RestartInPlaceHandsOffAndReattaches(t *testing.T) {
	f := newInPlaceFixtureT(t, inPlaceNewSelf)

	call, out := f.handOff(t)
	f.assertGCRoots(t, "after the handoff", "/nix/store/aaa-old", "/nix/store/bbb-new")
	if call.path != inPlaceNewSelf || call.argv[0] != inPlaceNewSelf {
		t.Errorf("exec path = %q argv0 = %q, want %q", call.path, call.argv[0], inPlaceNewSelf)
	}
	n := len(call.argv)
	if n < 3 || call.argv[n-2] != handoffFlag {
		t.Fatalf("exec argv = %v, want it to end in %s <fd>", call.argv, handoffFlag)
	}
	if want := []string{"--restart-in-place", "dispatch"}; !slices.Equal(call.argv[1:n-2], want) {
		t.Errorf("exec operator args = %v, want %v", call.argv[1:n-2], want)
	}
	assertNoHandoffIn(t, "exec", call.env)
	// The successor records the handoff once it has re-attached, so a failed
	// exec never leaves one behind.
	if _, ok := findEvent(eventsIn(out), "self_update", "handoff"); ok {
		t.Errorf("the old image emitted a handoff event; output:\n%s", out.String())
	}
	assertLockHeld(t, f.gitDir, true)

	// The go-file is never written, so the inherited child can only end through
	// a SIGTERM forwarded by the successor; it is therefore still running when
	// the successor inherits it.
	f.mu.Lock()
	pid := f.children[0].Process.Pid
	f.mu.Unlock()
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("kill(%d, 0) = %v before the second image started: the child must still be running", pid, err)
	}
	if _, err := os.Stat(f.termFile); err == nil {
		t.Fatal("child already saw SIGTERM before the second image started")
	}

	// A stop delivered between the two runs: the earliest the successor can
	// catch one is the moment it installs its handler (a signal inside the
	// execve itself is dropped, see signals_test.go; one landing once the
	// successor's main has started is latched), so the latch it gets is
	// already closed.
	f.onInstall = func() <-chan struct{} {
		stopped := make(chan struct{})
		close(stopped)
		return stopped
	}
	// What the successor's wrapper does: it re-adds --input and exports its own path.
	t.Setenv("SPINDRIFT_DAEMON_PROGRAM", inPlaceNewSelf)
	code, out2 := f.start(append([]string{"--input", f.input}, call.argv[1:]...))
	if got := waitCode(t, code, out2); got != 0 {
		t.Fatalf("second image exited %d; output:\n%s", got, out2.String())
	}
	f.assertGCRoots(t, "after the successor started", "/nix/store/bbb-new", "")
	if b, err := os.ReadFile(f.termFile); err != nil || strings.TrimSpace(string(b)) != "term" {
		t.Errorf("child term marker = %q (err %v), want \"term\": the stop was not forwarded to the inherited child", b, err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("kill(%d, 0) = %v, want ESRCH: the inherited child was not drained", pid, err)
	}
	evs := eventsIn(out2)
	for _, name := range []string{"child_inherited", "child_finish"} {
		if _, ok := findEvent(evs, name, ""); !ok {
			t.Errorf("second image emitted no %s event; output:\n%s", name, out2.String())
		}
	}
	ev, ok := findEvent(evs, "self_update", "handoff")
	if !ok {
		t.Fatalf("second image emitted no self_update handoff event; output:\n%s", out2.String())
	}
	if ev.FromPath != inPlaceOldSelf || ev.ToPath != inPlaceNewSelf || ev.Revision == "" || !slices.Equal(ev.Slots, []int{0}) {
		t.Errorf("handoff event = %+v, want from the old to the new path, a revision and slots [0]", ev)
	}
	f.mu.Lock()
	assertNoHandoffIn(t, "child", f.children[0].Env)
	f.mu.Unlock()
	report, err := daemon.ReadStatus(f.gitDir)
	if err != nil || report.Status == nil || report.Status.Pid != os.Getpid() {
		t.Errorf("status after the second image = %+v (err %v), want pid %d", report.Status, err, os.Getpid())
	}
	assertLockHeld(t, f.gitDir, false)
}

// TestMainRun_ResumeTakesOverBeforeParsingInput pins that a successor inherits
// the lock and the children ahead of everything that can fail: with no input
// document it still keeps the lock until the child it inherited is waited out.
func TestMainRun_ResumeTakesOverBeforeParsingInput(t *testing.T) {
	f := newInPlaceFixtureT(t, inPlaceNewSelf)
	call, _ := f.handOff(t)
	// No way back: a rollback would hand the child on instead of draining it.
	f.execErr = errors.New("no rollback")
	installed := f.installs.Load()
	f.mu.Lock()
	pid := f.children[0].Process.Pid
	f.mu.Unlock()

	code, out := f.start(append([]string{"--input", filepath.Join(t.TempDir(), "missing.json")}, call.argv[1:]...))
	select {
	case got := <-code:
		t.Fatalf("second image exited %d while its inherited child still runs; output:\n%s", got, out.String())
	case <-time.After(200 * time.Millisecond):
	}
	assertLockHeld(t, f.gitDir, true)
	if n := f.installs.Load(); n != installed+1 {
		t.Errorf("stop latch installs by the second image = %d, want 1: it must be latched before the input document is read", n-installed)
	}

	writeFileT(t, f.goFile, "")
	if got := waitCode(t, code, out); got != 1 {
		t.Fatalf("second image exited %d, want 1 (no input document); output:\n%s", got, out.String())
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("kill(%d, 0) = %v, want ESRCH: the inherited child was not reaped", pid, err)
	}
	assertLockHeld(t, f.gitDir, false)
}

// TestMainRun_ResumeThatFailsStartupStillHonoursStop pins that a successor
// waiting out the children it inherited, because its startup failed, forwards
// a stop that arrives during the wait: the operator's Ctrl-C must still reach
// them rather than sit in a latch nothing reads.
func TestMainRun_ResumeThatFailsStartupStillHonoursStop(t *testing.T) {
	f := newInPlaceFixtureT(t, inPlaceNewSelf)
	call, _ := f.handOff(t)
	f.execErr = errors.New("no rollback")
	f.mu.Lock()
	pid := f.children[0].Process.Pid
	f.mu.Unlock()

	stop := make(chan struct{})
	f.onInstall = func() <-chan struct{} { return stop }
	code, out := f.start(append([]string{"--input", filepath.Join(t.TempDir(), "missing.json")}, call.argv[1:]...))
	select {
	case got := <-code:
		t.Fatalf("second image exited %d while its inherited child still runs; output:\n%s", got, out.String())
	case <-time.After(200 * time.Millisecond):
	}

	close(stop)
	if got := waitCode(t, code, out); got != 1 {
		t.Fatalf("second image exited %d, want 1 (no input document); output:\n%s", got, out.String())
	}
	if b, err := os.ReadFile(f.termFile); err != nil || strings.TrimSpace(string(b)) != "term" {
		t.Errorf("child term marker = %q (err %v), want \"term\": the stop was not forwarded to the inherited child", b, err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("kill(%d, 0) = %v, want ESRCH: the inherited child was not drained", pid, err)
	}
	assertLockHeld(t, f.gitDir, false)
}

// TestMainRun_RestartInPlaceExecFailureKeepsSupervising pins that an exec that
// fails, or a target the exec refuses, blocks the update and hands the same
// children back to a fresh pool run instead of dropping them.
func TestMainRun_RestartInPlaceExecFailureKeepsSupervising(t *testing.T) {
	tests := []struct {
		name       string
		newSelf    string
		execErr    error
		wantReason string
		wantExecs  int
	}{
		{"exec error", inPlaceNewSelf, errors.New("boom"), "exec: boom", 1},
		{"target outside the store", "/opt/bbb-new/bin/daemon", nil, "exec: prebuilt daemon path \"/opt/bbb-new/bin/daemon\" is not under /nix/store/", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newInPlaceFixtureT(t, tt.newSelf)
			f.execErr = tt.execErr
			stop := make(chan struct{})
			f.stop = stop

			code, out := f.start([]string{"--input", f.input, "--restart-in-place", "dispatch"})
			ev := waitForEvent(t, out, "self_update", "blocked")
			if ev.Reason != tt.wantReason {
				t.Errorf("blocked reason = %q, want %q", ev.Reason, tt.wantReason)
			}
			close(stop)
			if got := waitCode(t, code, out); got != 0 {
				t.Fatalf("mainRun exited %d, want 0; output:\n%s", got, out.String())
			}
			evs := eventsIn(out)
			if _, ok := findEvent(evs, "self_update", "handoff"); ok {
				t.Errorf("a handoff event was recorded though the exec failed; output:\n%s", out.String())
			}
			for _, name := range []string{"child_inherited", "child_finish"} {
				if _, ok := findEvent(evs, name, ""); !ok {
					t.Errorf("no %s event after the failed exec; output:\n%s", name, out.String())
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.execs) != tt.wantExecs {
				t.Errorf("exec calls = %d, want %d", len(f.execs), tt.wantExecs)
			}
			if len(f.children) != 1 {
				t.Errorf("children started = %d, want 1: the pool must re-attach the first, not start another", len(f.children))
			}
		})
	}
}

// shutdownStallWriter passes writes through to out, except that once both the
// self_update built event and the child's first box report are written (the
// last two before the commit, in either order) it latches stop, and every
// shutdown event then stalls for hold, as a slow stdout consumer would. The
// Emitter serializes writes, so the flags need no lock of their own.
type shutdownStallWriter struct {
	out             *lockedBuffer
	stop            chan struct{}
	hold            time.Duration
	release         chan struct{}
	built, reported bool
	latched         bool
}

func (w *shutdownStallWriter) Write(p []byte) (int, error) {
	ev, _ := mergedEvent(strings.TrimSpace(string(p)))
	if ev.Event == "shutdown" && w.latched {
		<-w.release
	}
	n, err := w.out.Write(p)
	w.built = w.built || (ev.Event == "self_update" && ev.Phase == "built")
	w.reported = w.reported || ev.Event == "box"
	if w.built && w.reported && !w.latched {
		w.latched = true
		close(w.stop)
		time.AfterFunc(w.hold, func() { close(w.release) })
	}
	return n, err
}

// A SIGTERM latched just before the exec must refuse it even while the
// shutdown event is stuck in a stalled Emit, which holds the pool's own Stop
// open: the pre-exec re-check has to read the raw latch (issue #5279).
func TestMainRun_StopLatchedBeforeExecRefusesWhileShutdownEmitStalls(t *testing.T) {
	f := newInPlaceFixtureT(t, inPlaceNewSelf)
	setSignalSettle(t, 20*time.Millisecond)
	stop := make(chan struct{})
	f.stop = stop
	out := &lockedBuffer{}
	// hold is well past the settle, so an exec the stall failed to prevent has
	// run by the time the shutdown event lands.
	w := &shutdownStallWriter{out: out, stop: stop, hold: 500 * time.Millisecond, release: make(chan struct{})}

	code := make(chan int, 1)
	go func() { code <- mainRun([]string{"--input", f.input, "--restart-in-place", "dispatch"}, w, out) }()
	if got := waitCode(t, code, out); got != 0 {
		t.Fatalf("mainRun exited %d, want 0; output:\n%s", got, out.String())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.execs) != 0 {
		t.Errorf("exec calls = %d, want 0: the stop was latched before the exec; output:\n%s", len(f.execs), out.String())
	}
	evs := eventsIn(out)
	if _, ok := findEvent(evs, "shutdown", ""); !ok {
		t.Errorf("no shutdown event; output:\n%s", out.String())
	}
	builds := 0
	for _, ev := range evs {
		if ev.Event == "self_update" && ev.Phase == "built" {
			builds++
		}
	}
	if builds != 1 {
		t.Errorf("built events = %d, want 1: the refused handoff was recommitted; output:\n%s", builds, out.String())
	}
}

// TestMainRun_WithoutRestartInPlaceDrainsAndExits10 pins that the in-place
// restart is strictly opt-in: the same self-change without the flag prebuilds
// the successor, drains the pool and exits ExitSelfChanged, never exec'ing.
func TestMainRun_WithoutRestartInPlaceDrainsAndExits10(t *testing.T) {
	f := newInPlaceFixtureT(t, inPlaceNewSelf)

	code, out := f.start([]string{"--input", f.input, "dispatch"})
	waitForEvent(t, out, "self_update", "built")
	writeFileT(t, f.goFile, "")
	if got := waitCode(t, code, out); got != daemon.ExitSelfChanged {
		t.Fatalf("mainRun exited %d, want %d; output:\n%s", got, daemon.ExitSelfChanged, out.String())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.execs) != 0 {
		t.Errorf("exec calls = %d, want 0 without --restart-in-place", len(f.execs))
	}
	if _, ok := findEvent(eventsIn(out), "self_update", "handoff"); ok {
		t.Errorf("a handoff event was recorded without --restart-in-place; output:\n%s", out.String())
	}
}

func TestSplitHandoffFlag(t *testing.T) {
	argv := []string{"--input", "d", "dispatch"}
	rest, _, found, err := splitHandoffFlag(argv)
	if err != nil || found || !slices.Equal(rest, argv) {
		t.Errorf("splitHandoffFlag(no flag) = %v, %v, %v; want argv unchanged, not found", rest, found, err)
	}
	rest, raw, found, err := splitHandoffFlag([]string{"a", handoffFlag, "5", "b", handoffFlag, "7"})
	if err != nil || !found || raw != "7" || !slices.Equal(rest, []string{"a", "b"}) {
		t.Errorf("splitHandoffFlag(repeated) = %v, %q, %v, %v; want [a b], the last value", rest, raw, found, err)
	}
	if _, _, _, err := splitHandoffFlag([]string{handoffFlag}); err == nil {
		t.Error("splitHandoffFlag(flag without a value) succeeded")
	}
}

func TestResumeFromHandoff_RefusesBadDescriptor(t *testing.T) {
	for _, bad := range []string{"abc", "1", "-4"} {
		if res, err := resumeFromHandoff(bad); err == nil || res != nil {
			t.Errorf("resumeFromHandoff(%q) = %v, %v; want an error", bad, res, err)
		}
	}
}

// TestMainRun_RestartInPlaceRefusedWhereUnsupported pins that the refusal
// fires before the input document is read: the path does not exist, yet the
// error names the platform limit rather than a load failure.
func TestMainRun_RestartInPlaceRefusedWhereUnsupported(t *testing.T) {
	prev := inPlaceRestartSupported
	inPlaceRestartSupported = false
	t.Cleanup(func() { inPlaceRestartSupported = prev })

	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"--input", filepath.Join(t.TempDir(), "absent.json"), "--restart-in-place"}, &stdout, &stderr)
	if got != 1 {
		t.Errorf("mainRun() = %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), errInPlaceUnsupported.Error()) {
		t.Errorf("stderr = %q, want it to carry %q", stderr.String(), errInPlaceUnsupported)
	}
}

// failDoctor makes the next images' doctor preflight exit non-zero until the
// test ends.
func failDoctor(t *testing.T) {
	t.Helper()
	prev := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = prev })
	runnerDoctorCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 1")
	}
}

// pollUntil reports whether cond came true within five seconds.
func pollUntil(cond func() bool) bool {
	for range 500 {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// assertStillRunning fails the test if mainRun exits within a short grace: the
// image is expected to be waiting out its inherited child.
func assertStillRunning(t *testing.T, code <-chan int, out *lockedBuffer) {
	t.Helper()
	select {
	case got := <-code:
		t.Fatalf("image exited %d while its inherited child still runs; output:\n%s", got, out.String())
	case <-time.After(200 * time.Millisecond):
	}
}

// resumeArgv is what an image's wrapper runs the exec'd program with: the
// operator's argv from the exec call, with --input re-added.
func (f *inPlaceFixture) resumeArgv(call selfExecCall) []string {
	return append([]string{"--input", f.input}, call.argv[1:]...)
}

// gcRoot is where the named out-link under the state directory points, "" if
// it is absent.
func (f *inPlaceFixture) gcRoot(name string) string {
	target, err := os.Readlink(filepath.Join(f.gitDir, name))
	if err != nil {
		return ""
	}
	return target
}

// assertGCRoots checks the two daemon roots: current is the running build or
// the rollback target, next the prebuilt candidate ("" for none).
func (f *inPlaceFixture) assertGCRoots(t *testing.T, when, current, next string) {
	t.Helper()
	if got := f.gcRoot(currentGCRoot); got != current {
		t.Errorf("%s: %s -> %q, want %q", when, currentGCRoot, got, current)
	}
	if got := f.gcRoot(prebuildGCRoot); got != next {
		t.Errorf("%s: %s -> %q, want %q", when, prebuildGCRoot, got, next)
	}
}

func (f *inPlaceFixture) execCalls() []selfExecCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.execs)
}

// refuseAndRollBack hands off, then starts the second image with a doctor that
// fails, and returns the rollback exec the second image made. The second image
// has exited 0, still holding the lock for the third.
func (f *inPlaceFixture) refuseAndRollBack(t *testing.T) selfExecCall {
	t.Helper()
	call, _ := f.handOff(t)
	f.assertGCRoots(t, "after the handoff", "/nix/store/aaa-old", "/nix/store/bbb-new")
	// The third image must start with a working doctor.
	prevDoctor := runnerDoctorCommand
	defer func() { runnerDoctorCommand = prevDoctor }()
	failDoctor(t)
	t.Setenv("SPINDRIFT_DAEMON_PROGRAM", inPlaceNewSelf)
	code, out := f.start(f.resumeArgv(call))
	if got := waitCode(t, code, out); got != 0 {
		t.Fatalf("refusing image exited %d, want 0 (rolled back); output:\n%s", got, out.String())
	}
	calls := f.execCalls()
	if len(calls) != 2 {
		t.Fatalf("exec calls = %d, want 2 (handoff, rollback); output:\n%s", len(calls), out.String())
	}
	back := calls[1]
	n := len(back.argv)
	if back.path != inPlaceOldSelf || back.argv[0] != back.path || n < 3 || back.argv[n-2] != handoffFlag {
		t.Fatalf("rollback exec = %+v, want the old program with %s <fd>", back, handoffFlag)
	}
	assertNoHandoffIn(t, "rollback exec", back.env)
	f.assertGCRoots(t, "after the refusal", "/nix/store/aaa-old", "/nix/store/bbb-new")
	if _, ok := findEvent(eventsIn(out), "self_update", "rolled-back"); ok {
		t.Errorf("the refusing image emitted the rolled-back event itself; output:\n%s", out.String())
	}
	if _, ok := findEvent(eventsIn(out), "halt", ""); ok {
		t.Errorf("a rolled-back image left a halt event behind; output:\n%s", out.String())
	}
	assertLockHeld(t, f.gitDir, true)
	return back
}

// TestMainRun_ResumedImageThatRefusesToStartRollsBack drives three images: the
// first hands off, the second re-attaches but fails its doctor preflight and
// execs back to the first's build, and the third resumes from that document,
// records the rollback, keeps the inherited child and does not retry the
// revision it was rolled back from.
func TestMainRun_ResumedImageThatRefusesToStartRollsBack(t *testing.T) {
	f := newInPlaceFixtureT(t, inPlaceNewSelf)
	var prebuilds atomic.Int32
	origPrebuild := runnerPrebuildCommand
	runnerPrebuildCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "nix" && strings.HasSuffix(args[len(args)-1], "^out") {
			prebuilds.Add(1)
		}
		return origPrebuild(ctx, name, args...)
	}
	// One per tip resolution, counted when it starts.
	var resolves atomic.Int32
	origFetch := runnerFetchCommand
	t.Cleanup(func() { runnerFetchCommand = origFetch })
	runnerFetchCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if slices.Contains(args, "rev-parse") {
			resolves.Add(1)
		}
		return origFetch(ctx, name, args...)
	}
	back := f.refuseAndRollBack(t)
	failedPrebuilds := prebuilds.Load()

	stop := make(chan struct{})
	f.stop = stop
	t.Setenv("SPINDRIFT_DAEMON_PROGRAM", inPlaceOldSelf)
	code, out := f.start(f.resumeArgv(back))
	ev := waitForEvent(t, out, "self_update", "rolled-back")
	if ev.FromPath != inPlaceNewSelf || ev.ToPath != inPlaceOldSelf || ev.Revision == "" || !slices.Equal(ev.Slots, []int{0}) {
		t.Errorf("rolled-back event = %+v, want from the failed build to the old one, a revision and slots [0]", ev)
	}
	if !strings.Contains(ev.Reason, "preflight") {
		t.Errorf("rolled-back reason = %q, want the preflight refusal", ev.Reason)
	}
	if _, ok := findEvent(eventsIn(out), "self_update", "handoff"); ok {
		t.Errorf("the rolled-back image also recorded a handoff; output:\n%s", out.String())
	}
	waitForEvent(t, out, "child_inherited", "")
	f.assertGCRoots(t, "after the rollback", "/nix/store/aaa-old", "")

	var su *daemon.SelfUpdate
	pollUntil(func() bool {
		r, err := daemon.ReadStatus(f.gitDir)
		if err != nil || r.Status == nil {
			return false
		}
		su = r.Status.SelfUpdate
		return su != nil && su.Phase == daemon.SelfUpdateBlocked
	})
	if su == nil || su.Phase != daemon.SelfUpdateBlocked || su.Revision != ev.Revision {
		t.Errorf("status self_update = %+v, want blocked at %s", su, ev.Revision)
	}

	seen := resolves.Load()
	writeFileT(t, f.goFile, "")
	waitForEvent(t, out, "child_finish", "")
	// The tip is still the refused revision. The second resolution starts only
	// after the first one's self-update decision, so a retry would have shown
	// by then: as a prebuild, a building event or an exec, after which the
	// image resolves no more.
	if !pollUntil(func() bool {
		_, building := findEvent(eventsIn(out), "self_update", "building")
		return resolves.Load() >= seen+2 || building || prebuilds.Load() != failedPrebuilds || len(f.execCalls()) != 2
	}) {
		t.Fatalf("the rolled-back image resolved the tip %d times after its child finished, want 2; output:\n%s", resolves.Load()-seen, out.String())
	}
	close(stop)
	if got := waitCode(t, code, out); got != 0 {
		t.Fatalf("rolled-back image exited %d, want 0; output:\n%s", got, out.String())
	}
	if n := len(f.execCalls()); n != 2 {
		t.Errorf("exec calls = %d, want 2: the rolled-back image must not retry the failed revision", n)
	}
	if n := prebuilds.Load(); n != failedPrebuilds {
		t.Errorf("prebuilds after the rollback = %d, want %d: the failed revision was rebuilt", n, failedPrebuilds)
	}
	if _, ok := findEvent(eventsIn(out), "self_update", "building"); ok {
		t.Errorf("the failed revision was built again; output:\n%s", out.String())
	}
	assertLockHeld(t, f.gitDir, false)
}

// TestMainRun_RollbackImpossibleDrainsAndExits pins that a refusal whose
// rollback exec fails ends the way it did before rollback existed: the
// inherited child is waited out and the exit code is the refusal's.
func TestMainRun_RollbackImpossibleDrainsAndExits(t *testing.T) {
	f := newInPlaceFixtureT(t, inPlaceNewSelf)
	call, _ := f.handOff(t)
	f.execErr = errors.New("boom")
	failDoctor(t)
	code, out := f.start(f.resumeArgv(call))
	pollUntil(func() bool { return len(f.execCalls()) == 2 })
	if n := len(f.execCalls()); n != 2 {
		t.Fatalf("exec calls = %d, want the rollback attempt; output:\n%s", n, out.String())
	}
	assertStillRunning(t, code, out)
	assertLockHeld(t, f.gitDir, true)
	writeFileT(t, f.goFile, "")
	if got := waitCode(t, code, out); got != daemon.ExitPreflightFailed {
		t.Fatalf("image exited %d, want %d; output:\n%s", got, daemon.ExitPreflightFailed, out.String())
	}
	if _, ok := findEvent(eventsIn(out), "self_update", "rolled-back"); ok {
		t.Errorf("a rolled-back event was recorded though the exec failed; output:\n%s", out.String())
	}
	assertLockHeld(t, f.gitDir, false)
}

// TestMainRun_RollbackRefusedForOlderPredecessor pins that an image resumed
// from a version 1 or 2 document, whose writer cannot read every rollback
// field, drains its inherited child and exits as if rollback did not exist.
func TestMainRun_RollbackRefusedForOlderPredecessor(t *testing.T) {
	for n := 1; n < daemon.HandoffRollbackVersion; n++ {
		v := strconv.Itoa(n)
		t.Run("v"+v, func(t *testing.T) { rollbackRefusedForVersion(t, v) })
	}
}

func rollbackRefusedForVersion(t *testing.T, v string) {
	t.Helper()
	f := newInPlaceFixtureT(t, inPlaceNewSelf)
	call, _ := f.handOff(t)
	fd, err := strconv.Atoi(call.argv[len(call.argv)-1])
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(os.NewFile(uintptr(fd), "handoff doc"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"v":3`)) {
		t.Fatalf("handoff document is not version 3: %s", raw)
	}
	olderFD, err := handoffDocPipe(bytes.Replace(raw, []byte(`"v":3`), []byte(`"v":`+v), 1))
	if err != nil {
		t.Fatal(err)
	}
	call.argv = slices.Clone(call.argv)
	call.argv[len(call.argv)-1] = strconv.Itoa(olderFD)

	failDoctor(t)
	code, out := f.start(f.resumeArgv(call))
	assertStillRunning(t, code, out)
	writeFileT(t, f.goFile, "")
	if got := waitCode(t, code, out); got != daemon.ExitPreflightFailed {
		t.Fatalf("image exited %d, want %d; output:\n%s", got, daemon.ExitPreflightFailed, out.String())
	}
	if n := len(f.execCalls()); n != 1 {
		t.Errorf("exec calls = %d, want 1: no rollback exec for a version %s predecessor", n, v)
	}
	assertLockHeld(t, f.gitDir, false)
}

// TestMainRun_RolledBackImageDoesNotRollBackAgain pins that the build rolled
// back to, if it too refuses to start, drains and exits rather than handing
// the children back and forth.
func TestMainRun_RolledBackImageDoesNotRollBackAgain(t *testing.T) {
	f := newInPlaceFixtureT(t, inPlaceNewSelf)
	back := f.refuseAndRollBack(t)
	failDoctor(t)
	t.Setenv("SPINDRIFT_DAEMON_PROGRAM", inPlaceOldSelf)
	code, out := f.start(f.resumeArgv(back))
	writeFileT(t, f.goFile, "")
	if got := waitCode(t, code, out); got != daemon.ExitPreflightFailed {
		t.Fatalf("rolled-back image exited %d, want %d; output:\n%s", got, daemon.ExitPreflightFailed, out.String())
	}
	if n := len(f.execCalls()); n != 2 {
		t.Errorf("exec calls = %d, want 2: a rolled-back image must not roll back again", n)
	}
	assertLockHeld(t, f.gitDir, false)
}

// TestMainRun_ConfigRefusalAfterResumeRollsBack pins that every refusal after
// the re-attach rolls back, not only the preflight's: here the new build
// cannot load its input document.
func TestMainRun_ConfigRefusalAfterResumeRollsBack(t *testing.T) {
	f := newInPlaceFixtureT(t, inPlaceNewSelf)
	call, _ := f.handOff(t)
	code, out := f.start(append([]string{"--input", filepath.Join(t.TempDir(), "missing.json")}, call.argv[1:]...))
	if got := waitCode(t, code, out); got != 0 {
		t.Fatalf("image exited %d, want 0 (rolled back); output:\n%s", got, out.String())
	}
	calls := f.execCalls()
	if len(calls) != 2 || calls[1].path != inPlaceOldSelf {
		t.Fatalf("exec calls = %+v, want the handoff then a rollback to the old program", calls)
	}
	assertLockHeld(t, f.gitDir, true)
}

// kindMismatchArgv resumes the handed-off dispatch slot under a selector that
// no longer includes dispatch, a handoff-fit refusal (Config.Validate in
// mainRun) rather than a flag or input one.
func (f *inPlaceFixture) kindMismatchArgv(call selfExecCall) []string {
	argv := f.resumeArgv(call)
	i := slices.Index(argv, "dispatch")
	if i < 0 {
		panic("no dispatch selector in " + strings.Join(argv, " "))
	}
	argv[i] = "research"
	return argv
}

// TestMainRun_HandoffFitRefusalAfterResumeRollsBack pins that a handoff the
// new build's config cannot take (here the slot's kind left the selector)
// refuses like a failed preflight: it rolls back before any halt event, and
// the previous build keeps supervising the child.
func TestMainRun_HandoffFitRefusalAfterResumeRollsBack(t *testing.T) {
	f := newInPlaceFixtureT(t, inPlaceNewSelf)
	call, _ := f.handOff(t)
	code, out := f.start(f.kindMismatchArgv(call))
	if got := waitCode(t, code, out); got != 0 {
		t.Fatalf("image exited %d, want 0 (rolled back); output:\n%s", got, out.String())
	}
	calls := f.execCalls()
	if len(calls) != 2 || calls[1].path != inPlaceOldSelf {
		t.Fatalf("exec calls = %+v, want the handoff then a rollback to the old program", calls)
	}
	if _, ok := findEvent(eventsIn(out), "halt", ""); ok {
		t.Errorf("a rolled-back image left a halt event behind; output:\n%s", out.String())
	}
	r, err := daemon.ReadStatus(f.gitDir)
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	if r.Status != nil && r.Status.State == daemon.StateHalted {
		t.Errorf("a rolled-back image left a halted status: %+v", r.Status)
	}
	assertLockHeld(t, f.gitDir, true)

	// The old build is run with the original selector; the rollback exec only
	// forwarded the refusing image's.
	back := f.resumeArgv(calls[1])
	back[slices.Index(back, "research")] = "dispatch"
	stop := make(chan struct{})
	f.stop = stop
	t.Setenv("SPINDRIFT_DAEMON_PROGRAM", inPlaceOldSelf)
	code, out = f.start(back)
	ev := waitForEvent(t, out, "self_update", "rolled-back")
	if !strings.Contains(ev.Reason, "config-invalid") {
		t.Errorf("rolled-back reason = %q, want the config refusal", ev.Reason)
	}
	writeFileT(t, f.goFile, "")
	waitForEvent(t, out, "child_finish", "")
	close(stop)
	if got := waitCode(t, code, out); got != 0 {
		t.Fatalf("rolled-back image exited %d, want 0; output:\n%s", got, out.String())
	}
	assertLockHeld(t, f.gitDir, false)
}

// routedLocalInput rewrites the fixture's input document to route the local
// tracker at issuesDir. A path that is a regular file makes the tracker reject
// the routed count; a missing one counts zero.
func (f *inPlaceFixture) routedLocalInput(t *testing.T, issuesDir string) {
	t.Helper()
	doc := validKnobDocument()
	doc.Settings["DAEMON_SELF_APP"] = ".#dogfood"
	doc.Settings["ISSUE_TRACKER"] = "local"
	doc.Settings["ISSUE_ROUTE"] = "label=orders-api"
	doc.Settings["LOCAL_ISSUES_DIR"] = issuesDir
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.input, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestMainRun_RoutedTrackerRejectionHaltsConfigInvalid pins that a fresh
// routed start whose tracker rejects the startup count halts config-invalid
// before any Box, naming the route.
func TestMainRun_RoutedTrackerRejectionHaltsConfigInvalid(t *testing.T) {
	f := newInPlaceFixtureT(t, inPlaceNewSelf)
	issues := filepath.Join(t.TempDir(), "issues")
	writeFileT(t, issues, "not a directory")
	f.routedLocalInput(t, issues)
	code, out := f.start([]string{"--input", f.input, "dispatch"})
	if got := waitCode(t, code, out); got != 1 {
		t.Fatalf("image exited %d, want 1; output:\n%s", got, out.String())
	}
	ev, ok := findEvent(eventsIn(out), "halt", "")
	if !ok || !strings.Contains(ev.Reason, "config-invalid") {
		t.Errorf("halt event = %+v (found %v), want reason config-invalid; output:\n%s", ev, ok, out.String())
	}
	if !strings.Contains(out.String(), `ISSUE_ROUTE "label=orders-api"`) {
		t.Errorf("output does not name the route:\n%s", out.String())
	}
	if len(f.execCalls()) != 0 {
		t.Errorf("a refused start exec'd %+v", f.execCalls())
	}
	assertLockHeld(t, f.gitDir, false)
}

// TestMainRun_RoutedTrackerRejectionAfterResumeRollsBack pins that a resumed
// image whose routed startup count is rejected rolls back like a failed
// preflight instead of halting.
func TestMainRun_RoutedTrackerRejectionAfterResumeRollsBack(t *testing.T) {
	f := newInPlaceFixtureT(t, inPlaceNewSelf)
	issues := filepath.Join(t.TempDir(), "issues")
	// A ready issue on the route, or the first image would have no demand to
	// start the child it hands off.
	if err := os.Mkdir(issues, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, filepath.Join(issues, "a.md"), "---\ntitle: a\nstate: ready-for-agent\nlabels: [orders-api]\ncreated: 2024-01-01T00:00:00Z\n---\nbody\n")
	writeFileT(t, filepath.Join(issues, "b.md"), "---\ntitle: b\nstate: ready-for-agent\nlabels: [other]\ncreated: 2024-01-01T00:00:00Z\n---\nbody\n")
	f.routedLocalInput(t, issues)
	call, first := f.handOff(t)
	if want := `ISSUE_ROUTE "label=orders-api" matches 1 ready issue(s)`; !strings.Contains(first.String(), want) {
		t.Errorf("first image output lacks %q:\n%s", want, first.String())
	}
	if err := os.RemoveAll(issues); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, issues, "not a directory")
	code, out := f.start(f.resumeArgv(call))
	if got := waitCode(t, code, out); got != 0 {
		t.Fatalf("image exited %d, want 0 (rolled back); output:\n%s", got, out.String())
	}
	calls := f.execCalls()
	if len(calls) != 2 || calls[1].path != inPlaceOldSelf {
		t.Fatalf("exec calls = %+v, want the handoff then a rollback to the old program", calls)
	}
	if _, ok := findEvent(eventsIn(out), "halt", ""); ok {
		t.Errorf("a rolled-back image left a halt event behind; output:\n%s", out.String())
	}
	r, err := daemon.ReadStatus(f.gitDir)
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	if r.Status != nil && r.Status.State == daemon.StateHalted {
		t.Errorf("a rolled-back image left a halted status: %+v", r.Status)
	}
	assertLockHeld(t, f.gitDir, true)
}

// TestMainRun_HandoffFitRefusalWithoutRollbackHaltsAndDrains pins that with no
// rollback possible the same refusal halts config-invalid (exit 1, not the
// preflight's 11) and waits out the inherited child with the lock held.
func TestMainRun_HandoffFitRefusalWithoutRollbackHaltsAndDrains(t *testing.T) {
	f := newInPlaceFixtureT(t, inPlaceNewSelf)
	call, _ := f.handOff(t)
	f.execErr = errors.New("boom")
	code, out := f.start(f.kindMismatchArgv(call))
	pollUntil(func() bool { return len(f.execCalls()) == 2 })
	assertStillRunning(t, code, out)
	assertLockHeld(t, f.gitDir, true)
	writeFileT(t, f.goFile, "")
	if got := waitCode(t, code, out); got != 1 {
		t.Fatalf("image exited %d, want 1; output:\n%s", got, out.String())
	}
	if ev, ok := findEvent(eventsIn(out), "halt", ""); !ok || !strings.Contains(ev.Reason, "config-invalid") {
		t.Errorf("halt event = %+v (found %v), want reason config-invalid; output:\n%s", ev, ok, out.String())
	}
	r, err := daemon.ReadStatus(f.gitDir)
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	if r.Status == nil || r.Status.State != daemon.StateHalted {
		t.Errorf("published status = %+v, want state %q", r.Status, daemon.StateHalted)
	}
	assertLockHeld(t, f.gitDir, false)
}

// TestResumedRollBack_RefusesUnusableFromPath pins that a handoff document
// naming no usable previous program is refused before any exec.
func TestResumedRollBack_RefusesUnusableFromPath(t *testing.T) {
	tests := []struct {
		name, fromPath, wantErr string
	}{
		{"empty", "", "no previous daemon"},
		{"not clean", "/nix/store/aaa-old/../bbb/bin/daemon", "is not clean"},
		{"outside the store", "/usr/bin/daemon", "is not under /nix/store/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var execs atomic.Int32
			prev := runnerSelfExec
			t.Cleanup(func() { runnerSelfExec = prev })
			runnerSelfExec = func(string, []string, []string) error {
				execs.Add(1)
				return nil
			}
			res := &resumed{handoff: daemon.Handoff{V: daemon.HandoffRollbackVersion, FromPath: tt.fromPath, ToPath: inPlaceNewSelf}}
			err := res.rollBack(nil, nil, "refused")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("rollBack() = %v, want an error containing %q", err, tt.wantErr)
			}
			if n := execs.Load(); n != 0 {
				t.Errorf("exec calls = %d, want 0", n)
			}
		})
	}
}

func TestCapRollbackReason(t *testing.T) {
	if got := capRollbackReason("short"); got != "short" {
		t.Errorf("capRollbackReason(short) = %q, want it unchanged", got)
	}
	long := "x" + strings.Repeat("é", rollbackReasonMax) + "y"
	got := capRollbackReason(long)
	if !utf8.ValidString(got) {
		t.Errorf("capRollbackReason() = %q, want valid UTF-8", got)
	}
	if !strings.HasPrefix(got, "xé") || !strings.HasSuffix(got, "...") {
		t.Errorf("capRollbackReason() = %q..., want the head kept and a ... suffix", got[:10])
	}
	if len(got) > rollbackReasonMax+len("...") {
		t.Errorf("capRollbackReason() has len %d, want at most %d", len(got), rollbackReasonMax+len("..."))
	}
}

func claudeArtifacts() inputdoc.Artifacts {
	return inputdoc.Artifacts{Shared: inputdoc.SharedArtifacts{Driver: inputdoc.ValueOf("claude")}}
}

// The daemon applies the document's Driver rows right after loading it, so a
// malformed rows value refuses the start before any slot, claim or Box.
func TestMainRun_InvalidDriverRowsFailStartup(t *testing.T) {
	clearKnobEnvT(t)
	data, err := json.Marshal(inputdoc.Document{
		Settings: maps.Clone(shippedKnobDefaults),
		Drivers:  json.RawMessage(`"nope"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	docPath := filepath.Join(t.TempDir(), "in.json")
	writeFileT(t, docPath, string(data))

	var stdout, stderr bytes.Buffer
	if got := mainRun([]string{"--input", docPath, "dispatch"}, &stdout, &stderr); got != 1 {
		t.Fatalf("mainRun() = %d, want 1; stderr=%s", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "driver rows") {
		t.Errorf("stderr = %q, want the driver rows error", stderr.String())
	}
}

// The daemon applies the document's backend rows next to its Driver rows, so a
// row that fails validation refuses the start.
func TestMainRun_InvalidBackendRowsFailStartup(t *testing.T) {
	clearKnobEnvT(t)
	data, err := json.Marshal(inputdoc.Document{
		Settings: maps.Clone(shippedKnobDefaults),
		Backends: json.RawMessage(`[{"name":"local","module":"/m.wasm","credentialEnv":"T","routes":[{"method":"GET","host":"h.test","path":"/x"}]}]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	docPath := filepath.Join(t.TempDir(), "in.json")
	writeFileT(t, docPath, string(data))

	var stdout, stderr bytes.Buffer
	if got := mainRun([]string{"--input", docPath, "dispatch"}, &stdout, &stderr); got != 1 {
		t.Fatalf("mainRun() = %d, want 1; stderr=%s", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "backend rows") {
		t.Errorf("stderr = %q, want the backend rows error", stderr.String())
	}
}

// The daemon refuses to start on the removed read-write value rather than
// leave every child to fail on it.
func TestMainRun_RejectsReadWriteBoxAccess(t *testing.T) {
	for _, tc := range []struct {
		access   string
		wantExit int
	}{{"read-write", 1}, {"read-only", 0}} {
		t.Run(tc.access, func(t *testing.T) {
			clearKnobEnvT(t)
			checkOnlyNoNetworkT(t)
			docPath := writeInputDocT(t, map[string]string{
				"DAEMON_APP":                 ".#dogfood",
				"BASE_BRANCH":                "main",
				"MAX_PARALLEL":               "1",
				"BOX_FORGE_AND_ISSUE_ACCESS": tc.access,
			})
			var stdout, stderr bytes.Buffer
			if got := mainRun([]string{"--input", docPath, "--check-only", "dispatch"}, &stdout, &stderr); got != tc.wantExit {
				t.Fatalf("mainRun() = %d, want %d; stderr = %q", got, tc.wantExit, stderr.String())
			}
			if tc.wantExit == 0 {
				return
			}
			for _, want := range []string{"BOX_FORGE_AND_ISSUE_ACCESS=read-write was removed", "#6901", "MIGRATING.md"} {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
				}
			}
		})
	}
}
