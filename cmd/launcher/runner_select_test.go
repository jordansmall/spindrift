package main

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/runner"
)

// TestRunnerForKind_And_BuildRunnerForKind pins that both selectors key solely
// on c.runnerKind, never c.runtime (issue #2538 review finding): "bwrap" picks
// the bwrap adapter, and anything else, including "oci" and the empty default,
// picks the OCI adapter. The adapter types are unexported, so the test compares
// reflect.TypeOf against a runner from the matching exported constructor.
func TestRunnerForKind_And_BuildRunnerForKind(t *testing.T) {
	rc := runner.Config{}
	pwd := "/pwd"

	selectors := []struct {
		name string
		pick func(config, runner.Config, string) runner.Runner
	}{
		{name: "runnerForKind", pick: runnerForKind},
		{name: "buildRunnerForKind", pick: buildRunnerForKind},
	}

	for _, sel := range selectors {
		t.Run(sel.name, func(t *testing.T) {
			bwrapWant := runner.NewBwrap(rc, pwd)
			if sel.name == "buildRunnerForKind" {
				bwrapWant = runner.NewBwrapBuild(rc, pwd)
			}
			cases := []struct {
				name       string
				runnerKind string
				want       runner.Runner
			}{
				{name: "bwrap", runnerKind: "bwrap", want: bwrapWant},
				{name: "oci", runnerKind: "oci", want: runner.NewOCI(rc, pwd)},
				{name: "empty defaults to oci", runnerKind: "", want: runner.NewOCI(rc, pwd)},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					c := config{runnerKind: tc.runnerKind}
					got := sel.pick(c, rc, pwd)
					if reflect.TypeOf(got) != reflect.TypeOf(tc.want) {
						t.Errorf("%s(runnerKind=%q) = %T, want %T", sel.name, tc.runnerKind, got, tc.want)
					}
				})
			}
		})
	}
}
