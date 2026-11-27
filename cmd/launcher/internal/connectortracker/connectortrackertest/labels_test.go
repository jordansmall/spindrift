package connectortrackertest_test

import (
	"testing"

	"spindrift.dev/launcher/internal/connectortracker/connectortrackertest"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/inputdoc"
)

// schemaDefault fails, rather than skips, on a missing env row so a renamed knob cannot
// silently unpin the literal.
func schemaDefault(t *testing.T, env string) string {
	t.Helper()
	for _, e := range inputdoc.SchemaFlags {
		if e.Env == env {
			return e.Dflt
		}
	}
	t.Fatalf("no %s row in inputdoc.SchemaFlags", env)
	return ""
}

func TestLabelsMatchEnvSchemaDefaults(t *testing.T) {
	l := connectortrackertest.Labels
	for _, c := range []struct{ env, got string }{
		{"LABEL", l.Dispatchable},
		{"IN_PROGRESS_LABEL", l.InProgress},
		{"COMPLETE_LABEL", l.Complete},
		{"FAILED_LABEL", l.Failed},
	} {
		if want := schemaDefault(t, c.env); c.got != want {
			t.Errorf("Labels for %s = %q, lib/env-schema.nix default is %q", c.env, c.got, want)
		}
	}
	// Ambiguous has no env knob; lib/labels.nix is its only source.
	if _, ok := doctor.TriageLabelMeta[l.Ambiguous]; !ok {
		t.Errorf("Labels.Ambiguous %q is not a lib/labels.nix label", l.Ambiguous)
	}
}
