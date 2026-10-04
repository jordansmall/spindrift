package main

import (
	"strings"
	"testing"
)

func TestStartBranchIsPrefixPlusKeyAndExported(t *testing.T) {
	f := newFixture(t)
	f.in.BranchPrefix = "agent/pr-"
	f.knobs["DISPATCH_KEY"] = "7"
	f.env.DispatchKey = "7"
	f.run()
	if got := f.knobs["BRANCH"]; got != "agent/pr-7" {
		t.Errorf("exported BRANCH = %q", got)
	}
	if got := f.recoverCfgs[0].Branch; got != "agent/pr-7" {
		t.Errorf("recovery branch = %q", got)
	}
	if got := f.assembledEnv.Branch; got != "agent/pr-7" {
		t.Errorf("assembled Branch = %q", got)
	}
}

func TestStartDevShellKnobsExportedWhenSet(t *testing.T) {
	f := newFixture(t)
	f.in.DevShellName, f.in.DevShellProbeTimeout = "ci", "300"
	f.run()
	if f.knobs["DEV_SHELL_NAME"] != "ci" || f.knobs["DEV_SHELL_PROBE_TIMEOUT"] != "300" {
		t.Errorf("knobs = %q %q", f.knobs["DEV_SHELL_NAME"], f.knobs["DEV_SHELL_PROBE_TIMEOUT"])
	}
}

func TestStartDriverBashTimeout(t *testing.T) {
	const names = "BASH_DEFAULT_TIMEOUT_MS BASH_MAX_TIMEOUT_MS"
	warning := func(v string) string {
		return "==> WARNING: DRIVER_BASH_TIMEOUT_MS='" + v + "' is not a positive integer (milliseconds) — leaving the Driver's own Bash timeout"
	}
	for _, tc := range []struct {
		name, ms, names string
		want            map[string]string
		warn            string
	}{
		{name: "valid across names", ms: "600000", names: names,
			want: map[string]string{"BASH_DEFAULT_TIMEOUT_MS": "600000", "BASH_MAX_TIMEOUT_MS": "600000"}},
		{name: "unset", ms: "", names: names},
		{name: "empty name list", ms: "600000", names: ""},
		{name: "zero", ms: "0", names: names, warn: warning("0")},
		{name: "leading zero", ms: "0500", names: names, warn: warning("0500")},
		{name: "negative", ms: "-5", names: names, warn: warning("-5")},
		{name: "duration", ms: "30m", names: names, warn: warning("30m")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.in.DriverBashTimeoutMS, f.in.DriverBashTimeoutEnv = tc.ms, tc.names
			f.run()
			for _, n := range strings.Fields(names) {
				if got := f.knobs[n]; got != tc.want[n] {
					t.Errorf("%s = %q, want %q", n, got, tc.want[n])
				}
			}
			if got := countLine(f.lines(), tc.warn); tc.warn != "" && got != 1 {
				t.Errorf("warning printed %d times, want once; stdout:\n%s", got, f.stdout())
			}
			if tc.warn == "" && strings.Contains(f.stdout(), "WARNING: DRIVER_BASH_TIMEOUT_MS") {
				t.Error("unexpected warning")
			}
		})
	}
}
