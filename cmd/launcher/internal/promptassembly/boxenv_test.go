package promptassembly

import (
	"reflect"
	"testing"
)

// boxEnvRow mirrors one row of lib/promptassembly-boxenv.nix (issue #2979):
// the Go field EnvFromEnviron populates, the Box env var it reads, and the
// read rule ("kind") that drives the generated loader line. Hand-typed here,
// not imported from Nix, so this test is the anti-vacuity root the generated
// cmd/launcher/internal/promptassembly/boxenv_gen.go is checked against —
// exactly as gates_test.go hand-types its expectations rather than reading
// lib/baked-skills.nix.
type boxEnvRow struct {
	field string
	env   string
	kind  string // "presence" | "string" | "int" | "equals1"
}

var boxEnvRows = []boxEnvRow{
	{"OrchestratorEnabled", "ORCHESTRATOR_ENABLED", "presence"},
	{"AgentsJSONTemplate", "AGENTS_JSON_TEMPLATE", "string"},
	{"FilerEnabled", "BOX_FILER_ENABLED", "presence"},
	{"WorkerProvisioned", "BOX_WORKER_PROVISIONED", "presence"},
	{"ScoutProvisioned", "BOX_SCOUT_PROVISIONED", "presence"},
	{"ReviewLoopInline", "BOX_REVIEW_LOOP_INLINE", "presence"},
	{"ReviewLoopOrchestrator", "BOX_REVIEW_LOOP_ORCHESTRATOR", "presence"},
	{"IssueTracker", "ISSUE_TRACKER", "string"},
	{"TrackerAxisRead", "BOX_TRACKER_AXIS_READ", "string"},
	{"TrackerAxisWrite", "BOX_TRACKER_AXIS_WRITE", "string"},
	{"TrackerAxisFiler", "BOX_TRACKER_AXIS_FILER", "string"},
	{"BoxWriteEnabled", "BOX_WRITE_ENABLED", "presence"},
	{"LocalIssueReference", "LOCAL_ISSUE_REFERENCE", "presence"},
	{"CodeForge", "CODE_FORGE", "string"},
	{"ForgeBackend", "BOX_FORGE_BACKEND", "string"},
	{"DispatchKind", "DISPATCH_KIND", "string"},
	{"SelfContained", "SELF_CONTAINED", "equals1"},
	{"FixPass", "FIX_PASS", "int"},
	{"ResumeAfterHold", "RESUME_AFTER_HOLD", "presence"},
	{"AutoFormat", "AUTO_FORMAT", "presence"},
	{"AutoLint", "AUTO_LINT", "presence"},
	{"CIFailureSummary", "CI_FAILURE_SUMMARY", "string"},
	{"IssueNumber", "ISSUE_NUMBER", "string"},
	{"IssueTitle", "ISSUE_TITLE", "string"},
	{"Branch", "BRANCH", "string"},
	{"BaseBranch", "BASE_BRANCH", "string"},
	{"InProgressLabel", "IN_PROGRESS_LABEL", "string"},
	{"CompleteLabel", "COMPLETE_LABEL", "string"},
	{"RunNonce", "RUN_NONCE", "string"},
	{"ResearchStatusEnum", "RESEARCH_STATUS_ENUM", "string"},
}

// setValueAndExpect returns the env var value to set for a row's kind, and
// the resulting field value EnvFromEnviron must produce from it.
func setValueAndExpect(row boxEnvRow) (setValue string, want interface{}) {
	switch row.kind {
	case "presence":
		return "anything", true
	case "string":
		return "value-for-" + row.field, "value-for-" + row.field
	case "int":
		return "7", 7
	case "equals1":
		return "1", true
	default:
		panic("setValueAndExpect: unknown kind " + row.kind)
	}
}

// zeroValueFor is the zero value EnvFromEnviron must leave an unset covered
// field at, keyed by kind rather than reflect.Zero(fieldType) — a row's kind
// alone determines its Go type (presence/equals1 -> bool, string -> string,
// int -> int), so this stays independent of Env's own field declarations.
func zeroValueFor(kind string) interface{} {
	switch kind {
	case "presence", "equals1":
		return false
	case "string":
		return ""
	case "int":
		return 0
	default:
		panic("zeroValueFor: unknown kind " + kind)
	}
}

// TestEnvFromEnviron covers every lib/promptassembly-boxenv.nix row (issue
// #2979): setting exactly the one env var a row reads from must produce an
// Env with exactly that field populated and every other covered field left
// at its zero value — the read-one-leave-rest-alone contract the generated
// boxenv_gen.go's EnvFromEnviron must satisfy.
func TestEnvFromEnviron(t *testing.T) {
	for _, row := range boxEnvRows {
		row := row
		t.Run(row.field, func(t *testing.T) {
			// This test itself runs inside a spindrift Box (issue #2979's own
			// dispatch), so the ambient OS environment already carries real
			// values for several of these vars (ISSUE_NUMBER, RUN_NONCE, ...).
			// Clear every covered var first so only the row under test
			// is actually set, regardless of what the outer dispatch left
			// behind — t.Setenv restores each var's prior value once this
			// subtest ends.
			for _, other := range boxEnvRows {
				t.Setenv(other.env, "")
			}

			setValue, want := setValueAndExpect(row)
			t.Setenv(row.env, setValue)

			got := EnvFromEnviron()
			gotVal := reflect.ValueOf(got).FieldByName(row.field).Interface()
			if gotVal != want {
				t.Errorf("EnvFromEnviron().%s = %v, want %v", row.field, gotVal, want)
			}

			for _, other := range boxEnvRows {
				if other.field == row.field {
					continue
				}
				otherVal := reflect.ValueOf(got).FieldByName(other.field).Interface()
				wantZero := zeroValueFor(other.kind)
				if otherVal != wantZero {
					t.Errorf("EnvFromEnviron().%s = %v, want zero value %v (only %s was set)", other.field, otherVal, wantZero, row.env)
				}
			}
		})
	}
}

// TestEnvFromEnviron_FixPassMalformed covers FixPass's degrade-on-error rule
// (mirroring cmd/launcher/main.go's atoiSchema helper, which this generated
// file can't call directly since that helper is private to package main):
// a non-numeric FIX_PASS must degrade to 0, not propagate a strconv error or
// panic.
func TestEnvFromEnviron_FixPassMalformed(t *testing.T) {
	t.Setenv("FIX_PASS", "not-a-number")

	got := EnvFromEnviron()
	if got.FixPass != 0 {
		t.Errorf("EnvFromEnviron().FixPass = %d, want 0 for malformed input", got.FixPass)
	}
}

// TestEnvFromEnviron_SelfContainedRequiresExactlyOne covers the equals1 kind's
// strict comparison: only the literal "1" satisfies it, matching
// entrypoint.sh's "$SELF_CONTAINED" == "1" check — any other non-empty value
// (e.g. "true") must NOT satisfy it.
func TestEnvFromEnviron_SelfContainedRequiresExactlyOne(t *testing.T) {
	t.Setenv("SELF_CONTAINED", "true")

	got := EnvFromEnviron()
	if got.SelfContained {
		t.Errorf("EnvFromEnviron().SelfContained = true for SELF_CONTAINED=%q, want false (only \"1\" satisfies equals1)", "true")
	}
}
