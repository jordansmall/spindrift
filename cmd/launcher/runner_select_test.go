package main

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/runner"
)

// TestRunnerForKind_And_BuildRunnerForKind covers bootstrap's/cmdReconcile's
// runnerForKind and build()'s buildRunnerForKind together (issue #2538
// review finding): both must key solely on c.runnerKind ("bwrap" selects the
// bwrap adapter, anything else — including "oci" and the empty-string
// default — selects the OCI adapter), never c.runtime; they differ only in
// which constructor the bwrap arm calls. Since the concrete adapter types
// (bwrapAdapter, ociAdapter) are unexported, this asserts via
// reflect.TypeOf against a runner built by calling the corresponding
// exported constructor directly.
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
			bwrapWant := runner.NewBwrap(rc)
			if sel.name == "buildRunnerForKind" {
				bwrapWant = runner.NewBwrapBuild(rc)
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
