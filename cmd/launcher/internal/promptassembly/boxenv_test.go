package promptassembly

import (
	"reflect"
	"testing"
)

// boxEnvRow mirrors one row of lib/promptassembly-boxenv.nix (issue #2979).
// It is hand-typed rather than imported from Nix so it stays an independent
// check on the generated boxenv_gen.go, like gates_test.go's expectations.
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
	{"SignalCarrier", "BOX_SIGNAL_CARRIER", "string"},
	{"SelfContained", "SELF_CONTAINED", "equals1"},
	{"FixPass", "FIX_PASS", "int"},
	{"ResumeAfterHold", "RESUME_AFTER_HOLD", "presence"},
	{"AutoFormat", "AUTO_FORMAT", "presence"},
	{"AutoLint", "AUTO_LINT", "presence"},
	{"CIFailureSummary", "CI_FAILURE_SUMMARY", "string"},
	{"IssueNumber", "ISSUE_NUMBER", "string"},
	{"IssueTitle", "ISSUE_TITLE", "string"},
	{"IssueText", "ISSUE_TEXT", "string"},
	{"Branch", "BRANCH", "string"},
	{"BaseBranch", "BASE_BRANCH", "string"},
	{"InProgressLabel", "IN_PROGRESS_LABEL", "string"},
	{"CompleteLabel", "COMPLETE_LABEL", "string"},
	{"RunNonce", "RUN_NONCE", "string"},
	{"ResearchStatusEnum", "RESEARCH_STATUS_ENUM", "string"},
	{"ReviewModelOverride", "BOX_REVIEW_MODEL_OVERRIDE", "string"},
	{"ReviewEffortOverride", "BOX_REVIEW_EFFORT_OVERRIDE", "string"},
}

// boxEnvKindSpec is the one definition per kind that setValueAndExpect and
// zeroValueFor both consult, so the four kind values live in a single table
// instead of two switches that could drift apart.
type boxEnvKindSpec struct {
	setValue func(row boxEnvRow) string
	want     func(row boxEnvRow) interface{}
	zero     interface{}
}

var boxEnvKinds = map[string]boxEnvKindSpec{
	"presence": {
		setValue: func(row boxEnvRow) string { return "anything" },
		want:     func(row boxEnvRow) interface{} { return true },
		zero:     false,
	},
	"string": {
		setValue: func(row boxEnvRow) string { return "value-for-" + row.field },
		want:     func(row boxEnvRow) interface{} { return "value-for-" + row.field },
		zero:     "",
	},
	"int": {
		setValue: func(row boxEnvRow) string { return "7" },
		want:     func(row boxEnvRow) interface{} { return 7 },
		zero:     0,
	},
	"equals1": {
		setValue: func(row boxEnvRow) string { return "1" },
		want:     func(row boxEnvRow) interface{} { return true },
		zero:     false,
	},
}

// TestBoxEnvKinds_CoversEveryRowKind is issue #2979's AC #3 coverage guard:
// the kind table can neither miss a kind setValueAndExpect or zeroValueFor
// would panic on nor keep an entry no row uses.
func TestBoxEnvKinds_CoversEveryRowKind(t *testing.T) {
	used := map[string]bool{}
	for _, row := range boxEnvRows {
		used[row.kind] = true
		if _, ok := boxEnvKinds[row.kind]; !ok {
			t.Errorf("row %s has kind %q missing from boxEnvKinds", row.field, row.kind)
		}
	}
	for kind := range boxEnvKinds {
		if !used[kind] {
			t.Errorf("boxEnvKinds has kind %q not used by any row in boxEnvRows", kind)
		}
	}
}

func setValueAndExpect(row boxEnvRow) (setValue string, want interface{}) {
	spec, ok := boxEnvKinds[row.kind]
	if !ok {
		panic("setValueAndExpect: unknown kind " + row.kind)
	}
	return spec.setValue(row), spec.want(row)
}

// zeroValueFor keys on kind rather than reflect.Zero(fieldType) because a
// row's kind alone determines its Go type (presence and equals1 are bool,
// string is string, int is int), which keeps it independent of Env's own
// field declarations.
func zeroValueFor(kind string) interface{} {
	spec, ok := boxEnvKinds[kind]
	if !ok {
		panic("zeroValueFor: unknown kind " + kind)
	}
	return spec.zero
}

// TestEnvFromEnviron covers every lib/promptassembly-boxenv.nix row (issue
// #2979): the generated EnvFromEnviron must populate only the field whose env
// var is set and leave every other covered field at its zero value.
func TestEnvFromEnviron(t *testing.T) {
	for _, row := range boxEnvRows {
		row := row
		t.Run(row.field, func(t *testing.T) {
			// This test runs inside a spindrift Box, whose ambient environment
			// already sets several of these vars (ISSUE_NUMBER, RUN_NONCE, and
			// others), so clear every covered var before setting the row's own.
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

// TestEnvFromEnviron_FixPassMalformed covers FixPass's degrade-on-error rule:
// a non-numeric FIX_PASS must degrade to 0, not propagate a strconv error or
// panic. It mirrors cmd/launcher/main.go's atoiSchema, which the generated
// file cannot call because that helper is private to package main.
func TestEnvFromEnviron_FixPassMalformed(t *testing.T) {
	t.Setenv("FIX_PASS", "not-a-number")

	got := EnvFromEnviron()
	if got.FixPass != 0 {
		t.Errorf("EnvFromEnviron().FixPass = %d, want 0 for malformed input", got.FixPass)
	}
}

// TestEnvFromEnviron_SelfContainedRequiresExactlyOne covers the equals1 kind:
// only the literal "1" satisfies it, matching entrypoint.sh's
// "$SELF_CONTAINED" == "1" check, so a value like "true" must not.
func TestEnvFromEnviron_SelfContainedRequiresExactlyOne(t *testing.T) {
	t.Setenv("SELF_CONTAINED", "true")

	got := EnvFromEnviron()
	if got.SelfContained {
		t.Errorf("EnvFromEnviron().SelfContained = true for SELF_CONTAINED=%q, want false (only \"1\" satisfies equals1)", "true")
	}
}
