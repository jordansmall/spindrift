package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestHaltString_MatchesDocumentedGrammar pins one Halt.String() case per
// HaltClass against the exact operator-facing string docs/reference.md
// documents — a rendering change here is a documented-behaviour change, not
// a rename.
func TestHaltString_MatchesDocumentedGrammar(t *testing.T) {
	revision := "abc123"
	path := "/nix/store/old-daemon"
	selfProgram := "/nix/store/new-daemon"
	selfChangedDetail := fmt.Sprintf("daemon build at %s is %s, running %s", revision, path, selfProgram)
	breakerDetail := fmt.Sprintf("%d failures within %s reached threshold %d", 3, "5m0s", 3)

	cases := []struct {
		name string
		h    Halt
		want string
	}{
		{"none", Halt{Class: HaltNone}, ""},
		{"operator-stop", Halt{Class: HaltOperatorStop, Detail: "ctx cancelled"}, "context-cancelled: ctx cancelled"},
		{"child-signalled", Halt{Class: HaltChildSignalled}, "outcome: signalled-stop"},
		{"child-host-tainted", Halt{Class: HaltChildHostTainted}, "outcome: host-tainted"},
		{"child-config-invalid", Halt{Class: HaltChildConfigInvalid}, "outcome: config-invalid"},
		{"self-changed", Halt{Class: HaltSelfChanged, Detail: selfChangedDetail}, "self-changed: " + selfChangedDetail},
		{"self-build", Halt{Class: HaltSelfBuild, Detail: "exec format error"}, "self-build: exec format error"},
		{"breaker", Halt{Class: HaltBreaker, Detail: breakerDetail}, "breaker: " + breakerDetail},
		{"invalid-config", Halt{Class: HaltInvalidConfig, Detail: "missing Kinds"}, "config-invalid: missing Kinds"},
		{"preflight", Halt{Class: HaltPreflight, Detail: "doctor exit 4: required triage labels are missing"}, "preflight: doctor exit 4: required triage labels are missing"},
		{"instance-lock", Halt{Class: HaltInstanceLock, Detail: "held by pid 123"}, "instance-lock: held by pid 123"},
	}
	for _, tc := range cases {
		if got := tc.h.String(); got != tc.want {
			t.Errorf("%s: Halt.String() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestHaltExitCode_PinsWireNumbers pins the literal exit code each
// HaltClass maps to. These integers are the daemon process's contract with
// a process supervisor, so a drift here is a wire-protocol break even
// though it compiles clean.
func TestHaltExitCode_PinsWireNumbers(t *testing.T) {
	cases := []struct {
		class HaltClass
		want  int
	}{
		{HaltNone, 1},
		{HaltOperatorStop, 0},
		{HaltChildSignalled, 0},
		{HaltChildHostTainted, 1},
		{HaltChildConfigInvalid, 1},
		{HaltSelfChanged, 10},
		{HaltSelfBuild, 1},
		{HaltBreaker, 1},
		{HaltInvalidConfig, 1},
		{HaltPreflight, 11},
		{HaltInstanceLock, 1},
	}
	for _, tc := range cases {
		h := Halt{Class: tc.class}
		if got := h.ExitCode(); got != tc.want {
			t.Errorf("Halt{Class: %v}.ExitCode() = %d, want %d", tc.class, got, tc.want)
		}
	}
	if ExitSelfChanged != 10 {
		t.Errorf("ExitSelfChanged = %d, want 10", ExitSelfChanged)
	}
	if ExitPreflightFailed != 11 {
		t.Errorf("ExitPreflightFailed = %d, want 11", ExitPreflightFailed)
	}
}

// TestHaltEvent_ReasonMatchesStringAndOmitsZeroFields pins Event()'s shape:
// Reason always equals String(), and a pre-pool halt (zero Kind/Revision)
// marshals with no "kind"/"revision" key at all — the byte-identity
// guarantee the three pre-pool halt call sites (config-invalid,
// instance-lock, preflight) depend on to match today's JSON exactly.
func TestHaltEvent_ReasonMatchesStringAndOmitsZeroFields(t *testing.T) {
	h := Halt{Class: HaltOperatorStop, Detail: "shutting down"}
	ev := h.Event()
	if ev.Event != "halt" {
		t.Fatalf("Event().Event = %q, want %q", ev.Event, "halt")
	}
	if ev.Reason != h.String() {
		t.Fatalf("Event().Reason = %q, want %q (String())", ev.Reason, h.String())
	}
	if ev.Kind != "" || ev.Revision != "" {
		t.Fatalf("Event() = %+v, want zero Kind/Revision", ev)
	}

	buf, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	body := string(buf)
	if strings.Contains(body, `"kind"`) || strings.Contains(body, `"revision"`) {
		t.Fatalf("marshalled Event has a kind/revision key with zero values: %s", body)
	}

	h2 := Halt{Class: HaltSelfChanged, Detail: "d", Kind: KindDispatch, Revision: "rev1"}
	ev2 := h2.Event()
	if ev2.Kind != KindDispatch || ev2.Revision != "rev1" {
		t.Fatalf("Event() = %+v, want Kind/Revision carried through", ev2)
	}
}

// TestHaltRenderings_CoversEveryClass guards against a HaltClass added later
// compiling clean with no row in haltRenderings — String()/ExitCode() would
// silently fall back to "" / 1 rather than failing to build.
func TestHaltRenderings_CoversEveryClass(t *testing.T) {
	if len(haltRenderings) != int(haltClassCount) {
		t.Fatalf("len(haltRenderings) = %d, want %d (haltClassCount)", len(haltRenderings), haltClassCount)
	}
	for c := HaltClass(0); c < haltClassCount; c++ {
		if _, ok := haltRenderings[c]; !ok {
			t.Errorf("HaltClass %v (%d) has no row in haltRenderings", c, c)
		}
	}
}
