package main

import (
	"testing"

	"spindrift.dev/launcher/internal/inputdoc"
)

func TestDispatchLabelsMatchEnvSchemaDefaults(t *testing.T) {
	defaults := map[string]string{}
	for _, e := range inputdoc.SchemaFlags {
		defaults[e.Env] = e.Dflt
	}
	l := testDispatchLabels
	for _, c := range []struct{ env, got string }{
		{"LABEL", l.Dispatchable},
		{"IN_PROGRESS_LABEL", l.InProgress},
		{"COMPLETE_LABEL", l.Complete},
		{"FAILED_LABEL", l.Failed},
	} {
		want, ok := defaults[c.env]
		if !ok {
			t.Fatalf("no %s row in inputdoc.SchemaFlags", c.env)
		}
		if c.got != want {
			t.Errorf("testDispatchLabels for %s = %q, lib/env-schema.nix default is %q", c.env, c.got, want)
		}
	}
}
