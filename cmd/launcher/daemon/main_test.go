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
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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
		name                 string
		args                 []string
		wantPath             string
		wantKinds            []daemon.Kind
		wantExplicitSelector bool
		wantFeatureBranch    string
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
			if got.InputPath != tt.wantPath || !slices.Equal(got.Kinds, tt.wantKinds) || got.ExplicitSelector != tt.wantExplicitSelector || got.FeatureBranch != tt.wantFeatureBranch {
				t.Errorf("parseArgs(%v) = %+v, want {InputPath:%q Kinds:%v ExplicitSelector:%v FeatureBranch:%q}", tt.args, got, tt.wantPath, tt.wantKinds, tt.wantExplicitSelector, tt.wantFeatureBranch)
			}
		})
	}
}

// TestGateKinds pins the descriptor-Enablement gating decision (ADR 0056, issue
// #3878): butler drops out of a bare "every kind" default when
// BUTLER_CHORES is empty, survives when it isn't, an explicit butler
// selector with no chores enabled fails instead of dropping silently, and a
// kind set that never had butler in it is untouched either way -- including
// when BUTLER_EVERY/BUTLER_CHORE_CLASSES are themselves malformed, since a
// research/dispatch-only daemon must never fail on butler config it never
// parses (issue #3991). With BUTLER_CHORES empty, chore.Load short-circuits
// before it ever looks at BUTLER_EVERY/BUTLER_CHORE_CLASSES, so a bare
// invocation drops the butler even with a leftover or malformed value in
// either -- an explicit `butler` selector still fails, since it wants the
// butler regardless of what got it there. Once a Chore is actually enabled,
// a chore.Load error fails startup outright.
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
		boxAccess        string // resolved BOX_FORGE_AND_ISSUE_ACCESS
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
			codeForge: "github", boxAccess: "read-only",
			want: []daemon.Kind{daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research), recoverKind},
		},
		{
			name:      "bare default keeps recover for forgejo read-only",
			kinds:     everyWithRecover,
			codeForge: "forgejo", boxAccess: "read-only",
			want: []daemon.Kind{daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research), recoverKind},
		},
		{
			name:      "bare default drops recover for local read-only",
			kinds:     everyWithRecover,
			codeForge: "local", boxAccess: "read-only",
			want: []daemon.Kind{daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research)},
		},
		{
			name:      "bare default drops recover for git read-only",
			kinds:     everyWithRecover,
			codeForge: "git", boxAccess: "read-only",
			want: []daemon.Kind{daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research)},
		},
		{
			name:      "bare default drops recover for github read-write",
			kinds:     everyWithRecover,
			codeForge: "github", boxAccess: "read-write",
			want: []daemon.Kind{daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research)},
		},
		{
			name:             "explicit recover with relay disabled fails on the sentinel",
			kinds:            []daemon.Kind{recoverKind},
			explicitSelector: true,
			codeForge:        "github", boxAccess: "read-write",
			wantErr:    true,
			wantErrIs:  errNoOutboxRelay,
			wantErrMsg: "recover selected but",
		},
		{
			name:             "explicit recover with relay enabled yields just recover",
			kinds:            []daemon.Kind{recoverKind},
			explicitSelector: true,
			codeForge:        "forgejo", boxAccess: "read-only",
			want: []daemon.Kind{recoverKind},
		},
		{
			name:             "explicit dispatch excludes recover whatever the relay",
			kinds:            []daemon.Kind{daemon.KindOf(dispatchkind.Work)},
			explicitSelector: true,
			codeForge:        "github", boxAccess: "read-only",
			want: []daemon.Kind{daemon.KindOf(dispatchkind.Work)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			knobs := gateKnobs{
				chores:    chore.Knobs{Chores: tt.butlerChores, Every: tt.butlerEvery, Classes: tt.butlerClasses},
				codeForge: tt.codeForge,
				boxAccess: tt.boxAccess,
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
	em := daemon.NewEmitter(&buf, io.Discard, time.Now)

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

// daemonKnobEnvVars is every env var the daemon package reads by name,
// including the wrapper's SPINDRIFT_DAEMON_PROGRAM, cleared to keep
// mainRun's self-change check off. TestClearKnobEnvT_CoversEveryKnob pins
// it to the call sites.
var daemonKnobEnvVars = []string{
	"DAEMON_APP", "BASE_BRANCH", "MAX_PARALLEL", "RESEARCH_RESERVATION",
	"DAEMON_IDLE_FLOOR", "DAEMON_IDLE_CAP", "DAEMON_FAILURE_BACKOFF",
	"DAEMON_BREAKER_THRESHOLD", "DAEMON_BREAKER_WINDOW", "BOX_SIGNAL_CARRIER",
	"MAX_RECOVER_ATTEMPTS", "TRANSIENT_BACKOFF_SECS", "DAEMON_AWAKE_WINDOW",
	"BUTLER_CHORES", "BUTLER_EVERY", "BUTLER_CHORE_CLASSES",
	"DAEMON_SELF_APP", "SPINDRIFT_DAEMON_PROGRAM", "DAEMON_PROBE_INTERVAL",
	"ISSUE_TRACKER", "LOCAL_ISSUES_DIR", "LABEL", "IN_PROGRESS_LABEL",
	"COMPLETE_LABEL", "FAILED_LABEL", "REPO_SLUG", "FORGEJO_BASE_URL", "FORGEJO_TOKEN",
	"GH_TOKEN_REFRESH_FILE", "JIRA_BASE_URL", "JIRA_PROJECT_KEY", "JIRA_EMAIL", "JIRA_TOKEN",
	"JIRA_STATUS_MAPPING", "FORGEJO_TOKEN_CMD", "JIRA_TOKEN_CMD",
	"CODE_FORGE", "BOX_FORGE_AND_ISSUE_ACCESS",
}

// clearKnobEnvT clears the daemon's knob env vars for the duration of the
// test, so an ambient export in the host/CI environment cannot shadow the
// input document values these tests set up.
func clearKnobEnvT(t *testing.T) {
	t.Helper()
	for _, v := range daemonKnobEnvVars {
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
// with no positional verb and no BUTLER_CHORES, gateKinds drops butler
// from parseArgs' every-kind default, leaving dispatch and research, so the
// same RESEARCH_RESERVATION=5/MAX_PARALLEL=1 document must now fail startup
// at the reservation check, before ever reaching
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

// TestPublishHaltedStatus pins what mainRun's preflight-refusal branch relies
// on: with no pool yet to snapshot through, a direct Write still lands a
// StateHalted status naming the same reason and kinds the caller passed.
func TestPublishHaltedStatus(t *testing.T) {
	dir := t.TempDir()
	sw := daemon.NewStatusWriter(dir, func() time.Time { return time.Unix(0, 0).UTC() })
	var stderr bytes.Buffer

	publishHaltedStatus(&stderr, sw, []daemon.Kind{daemon.KindOf(dispatchkind.Work)}, "preflight: doctor-config-invalid")

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
	if len(report.Status.Kinds) != 1 || report.Status.Kinds[0] != daemon.KindOf(dispatchkind.Work) {
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

// TestStartupPreflight_RunsExactlyOnce asserts RunDoctor is called exactly
// once across a whole startupPreflight call. The stronger structural claim —
// "called once per daemon startup, not once per Loop iteration" — is
// established by inspection of mainRun's call site (placed before
// daemon.Loop, outside any loop), not by this test alone.
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

// TestStartupPreflight_FeatureBranchGone pins that a confirmed-gone
// --feature-branch refuses startup the same way as any other resolve
// failure (issue #3883): Class stays HaltPreflight — HaltFeatureBranchGone
// is Loop's class, reached only once the pool is already running, never
// startup's — its rendered String() names both the branch and the remote,
// RunDoctor is never called, and the "preflight" event's outcome gets its
// own label distinct from an ordinary seam error.
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

// TestFinish_OnePathForEveryPreLoopHalt pins mainRun's single finish path
// (issue #3622 slice 3): a preflight refusal, an operator Ctrl-C during the
// preflight, and an instance-lock refusal each emit exactly one "halt"
// event carrying h.String()'s documented reason and return the exit code
// their HaltClass derives — 11, 0, and 1 respectively — print their
// h.Diagnostic() line to stderr unchanged, and only the first
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
// inputdoc.Document.Lookup itself applies (its `v := os.Getenv(envVar);
// v != ""` guard in inputdoc.go): an exported-but-empty knob (present in
// the environment with value "") is not "set" there, so it must not be
// "set" here either.
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

// TestMainRun_FeatureBranchPrintsStartupLineAndReachesDoctor pins issue
// #3882 end to end in one table, flag-set and flag-unset, matching
// TestParseArgs's style: --feature-branch wired from argv reaches the
// doctor preflight's argv as --base-branch and mainRun prints the one
// startup line naming both branches before the (stubbed, failing) preflight
// halts it, while an unset flag leaves both byte-for-byte absent.
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
				// fetchRevision now confirms --feature-branch exists on
				// origin (issue #3883) before this test ever reaches
				// RunDoctor; push the branch here so the preflight still
				// gets past that check to the doctor argv this test is
				// actually about.
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
// arms the marker file once the trap is installed.
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
	// Also disables the self-change check (SPINDRIFT_DAEMON_PROGRAM unset):
	// this configuration then never reaches runnerEvalCommand, so there is
	// nothing else to stub.
	clearKnobEnvT(t)

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
// line, so a terminal merging stdout and stderr reads event then diagnostic
// (the order before 580296cf), not the reverse.
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

// TestFinish_StatusWriteWarningTrailsDiagnostic pins the other half of issue
// #3711 at finish itself, with one writer standing in for a merged terminal:
// an operator cancel still reads halt event then diagnostic, and a failed
// status write's warning lands after both, never ahead of the halt
// diagnostic it qualifies. Checked at finish rather than mainRun because
// mainRun's status file lives in the git dir, which a test cannot make
// unwritable portably.
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
			em := daemon.NewEmitter(&merged, io.Discard, clock)
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

// TestStartupPreflight_ExitFourNamesMissingLabels pins that doctor's own
// labels line, not ClassifyPreflight's generic text, becomes the halt's
// detail: exit 4 now also covers the butler/research/Filer label sets.
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
