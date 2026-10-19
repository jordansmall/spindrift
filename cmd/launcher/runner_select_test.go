package main

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/bwrap"
	"spindrift.dev/launcher/internal/runner/oci"
	"spindrift.dev/launcher/internal/runner/runnertest"
)

// TestRunnerForKind_And_BuildRunnerForKind pins that both selectors key solely
// on c.runnerKind, never c.runtime (issue #2538 review finding): the runner
// registry resolves "bwrap" and "oci" (the empty default is OCI), and an
// unregistered kind is an error. The adapter types are unexported, so the test compares
// reflect.TypeOf against a runner from the matching exported constructor.
func TestRunnerForKind_And_BuildRunnerForKind(t *testing.T) {
	rc := runner.Config{}
	pwd := "/pwd"

	selectors := []struct {
		name string
		pick func(config, runner.Config, string) (runner.Runner, error)
	}{
		{name: "runnerForKind", pick: runnerForKind},
		{name: "buildRunnerForKind", pick: buildRunnerForKind},
	}

	for _, sel := range selectors {
		t.Run(sel.name, func(t *testing.T) {
			bwrapWant, err := bwrap.New(rc, config{}.knobLookup, pwd)
			if err != nil {
				t.Fatal(err)
			}
			ociWant, err := oci.New(rc, config{}.knobLookup, pwd)
			if err != nil {
				t.Fatal(err)
			}
			if sel.name == "buildRunnerForKind" {
				bwrapWant, err = bwrap.NewBuild(rc, config{}.knobLookup, pwd)
				if err != nil {
					t.Fatal(err)
				}
			}
			cases := []struct {
				name       string
				runnerKind string
				want       runner.Runner
			}{
				{name: "bwrap", runnerKind: "bwrap", want: bwrapWant},
				{name: "oci", runnerKind: "oci", want: ociWant},
				{name: "empty defaults to oci", runnerKind: "", want: ociWant},
			}
			t.Run("unregistered kind errors", func(t *testing.T) {
				got, err := sel.pick(config{runnerKind: "nspawn"}, rc, pwd)
				if err == nil {
					t.Fatalf("%s(runnerKind=nspawn) = %T, want an error", sel.name, got)
				}
				for _, want := range []string{"nspawn", "bwrap", "oci"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("%s(runnerKind=nspawn) error = %q, want it to name %q", sel.name, err, want)
					}
				}
			})
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					c := config{runnerKind: tc.runnerKind}
					got, err := sel.pick(c, rc, pwd)
					if err != nil {
						t.Fatalf("%s(runnerKind=%q) error = %v", sel.name, tc.runnerKind, err)
					}
					if reflect.TypeOf(got) != reflect.TypeOf(tc.want) {
						t.Errorf("%s(runnerKind=%q) = %T, want %T", sel.name, tc.runnerKind, got, tc.want)
					}
				})
			}
		})
	}
}

// TestRunnerForKind_BwrapUnshareNetKnobReachesAdapter pins the launcher's
// "non-empty BWRAP_UNSHARE_NET means on" bridge: NETWORK_MODE=host alone leaves
// the bwrap Box on the host network, so only the knob can isolate it, even for
// a value strconv.ParseBool would reject.
func TestRunnerForKind_BwrapUnshareNetKnobReachesAdapter(t *testing.T) {
	cases := []struct {
		name        string
		unshareNet  string
		wantIsolate bool
	}{
		{name: "unset", unshareNet: "", wantIsolate: false},
		{name: "1", unshareNet: "1", wantIsolate: true},
		{name: "yes", unshareNet: "yes", wantIsolate: true},
		{name: "0", unshareNet: "0", wantIsolate: true},
		{name: "false", unshareNet: "false", wantIsolate: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NETWORK_MODE", "host")
			t.Setenv("RUNNER_KIND", "bwrap")
			t.Setenv("BWRAP_UNSHARE_NET", tc.unshareNet)
			c := loadConfig()

			got, err := runnerForKind(c, runnerConfig(c), "/pwd")
			if err != nil {
				t.Fatal(err)
			}
			want, err := bwrap.New(runnerConfig(c), runnertest.Lookup(map[string]string{
				"BWRAP_UNSHARE_NET": strconv.FormatBool(tc.wantIsolate),
			}), "/pwd")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("adapter = %+v, want %+v (isolate=%v)", got, want, tc.wantIsolate)
			}
		})
	}
}
