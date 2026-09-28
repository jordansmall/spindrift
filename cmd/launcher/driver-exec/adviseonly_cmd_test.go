package main

import (
	"bytes"
	"testing"

	"spindrift.dev/launcher/internal/dispatchkind"
)

// Every known kind must round-trip through its own AdviseOnly bit, iterating
// over dispatchkind.All so the assertion never hardcodes a kind name.
func TestRunAdviseOnly_KnownKinds(t *testing.T) {
	for _, d := range dispatchkind.All {
		want := "0\n"
		if d.AdviseOnly {
			want = "1\n"
		}
		var stdout bytes.Buffer
		rc := runAdviseOnly([]string{"--dispatch-kind", d.Name}, &stdout)
		if rc != 0 {
			t.Fatalf("runAdviseOnly(%q) exit = %d, want 0", d.Name, rc)
		}
		if got := stdout.String(); got != want {
			t.Errorf("runAdviseOnly(%q) stdout = %q, want %q", d.Name, got, want)
		}
	}
}

// The flag default must be dispatchkind.Work.Name, mirroring
// outcome-backstop's --dispatch-kind: an omitted flag is work's posture.
func TestRunAdviseOnly_DefaultsToWork(t *testing.T) {
	var stdout bytes.Buffer
	rc := runAdviseOnly(nil, &stdout)
	if rc != 0 {
		t.Fatalf("runAdviseOnly(nil) exit = %d, want 0", rc)
	}
	if got, want := stdout.String(), "0\n"; got != want {
		t.Errorf("runAdviseOnly(nil) stdout = %q, want %q", got, want)
	}
}

// An unknown kind must fail closed: exit 1, no stdout, never defaulting to
// work's posture.
func TestRunAdviseOnly_UnknownKindFailsClosed(t *testing.T) {
	var stdout bytes.Buffer
	rc := runAdviseOnly([]string{"--dispatch-kind", "bogus"}, &stdout)
	if rc != 1 {
		t.Fatalf("runAdviseOnly(bogus) exit = %d, want 1", rc)
	}
	if got := stdout.String(); got != "" {
		t.Errorf("runAdviseOnly(bogus) stdout = %q, want empty", got)
	}
}

// A flag parse error exits 2, distinct from the unknown-kind exit 1.
func TestRunAdviseOnly_FlagParseErrorExitsTwo(t *testing.T) {
	var stdout bytes.Buffer
	rc := runAdviseOnly([]string{"--not-a-flag"}, &stdout)
	if rc != 2 {
		t.Fatalf("runAdviseOnly(--not-a-flag) exit = %d, want 2", rc)
	}
}

// Only a bare "advise-only" first arg claims the subcommand; every other arg
// shape falls through to the default Driver invocation.
func TestIsAdviseOnlyInvocation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"advise-only first arg", []string{"advise-only", "--dispatch-kind", "work"}, true},
		{"ordinary flag invocation", []string{"--prompt-file", "x"}, false},
		{"no args", nil, false},
	}
	for _, c := range cases {
		if got := isAdviseOnlyInvocation(c.args); got != c.want {
			t.Errorf("%s: isAdviseOnlyInvocation(%v) = %v, want %v", c.name, c.args, got, c.want)
		}
	}
}
