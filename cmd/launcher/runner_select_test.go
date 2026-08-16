package main

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/runner"
)

// TestRunnerForKind covers bootstrap's and cmdReconcile's shared runner
// selection helper (issue #2538 review finding): it must key solely on
// c.runnerKind ("bwrap" selects the bwrap adapter, anything else — including
// "oci" and the empty-string default — selects the OCI adapter), never
// c.runtime. Since the concrete adapter types (bwrapAdapter, ociAdapter) are
// unexported, this asserts via reflect.TypeOf against a runner built by
// calling the corresponding exported constructor directly.
func TestRunnerForKind(t *testing.T) {
	rc := runner.Config{}
	pwd := "/pwd"

	cases := []struct {
		name       string
		runnerKind string
		want       runner.Runner
	}{
		{name: "bwrap", runnerKind: "bwrap", want: runner.NewBwrap(rc)},
		{name: "oci", runnerKind: "oci", want: runner.NewOCI(rc, pwd)},
		{name: "empty defaults to oci", runnerKind: "", want: runner.NewOCI(rc, pwd)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := config{runnerKind: tc.runnerKind}
			got := runnerForKind(c, rc, pwd)
			if reflect.TypeOf(got) != reflect.TypeOf(tc.want) {
				t.Errorf("runnerForKind(runnerKind=%q) = %T, want %T", tc.runnerKind, got, tc.want)
			}
		})
	}
}

// TestBuildRunnerForKind is runnerForKind's `launcher build` counterpart
// (main.go's build()): the bwrap arm selects runner.NewBwrapBuild instead of
// runner.NewBwrap, but keys off the same c.runnerKind == "bwrap" check.
func TestBuildRunnerForKind(t *testing.T) {
	rc := runner.Config{}
	pwd := "/pwd"

	cases := []struct {
		name       string
		runnerKind string
		want       runner.Runner
	}{
		{name: "bwrap", runnerKind: "bwrap", want: runner.NewBwrapBuild(rc)},
		{name: "oci", runnerKind: "oci", want: runner.NewOCI(rc, pwd)},
		{name: "empty defaults to oci", runnerKind: "", want: runner.NewOCI(rc, pwd)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := config{runnerKind: tc.runnerKind}
			got := buildRunnerForKind(c, rc, pwd)
			if reflect.TypeOf(got) != reflect.TypeOf(tc.want) {
				t.Errorf("buildRunnerForKind(runnerKind=%q) = %T, want %T", tc.runnerKind, got, tc.want)
			}
		})
	}
}
