package main

import (
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/bwrap"
	"spindrift.dev/launcher/internal/runner/oci"
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
			bwrapWant := bwrap.New(rc, pwd)
			if sel.name == "buildRunnerForKind" {
				bwrapWant = bwrap.NewBuild(rc, pwd)
			}
			cases := []struct {
				name       string
				runnerKind string
				want       runner.Runner
			}{
				{name: "bwrap", runnerKind: "bwrap", want: bwrapWant},
				{name: "oci", runnerKind: "oci", want: oci.New(rc, pwd)},
				{name: "empty defaults to oci", runnerKind: "", want: oci.New(rc, pwd)},
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
