package promptassembly

import (
	"os"
	"strings"
	"testing"
)

// TestValidateNoGatesActive covers the baseline no-op case: every gate the
// four validateMarkers rows key off of is off (kind "work", box write
// enabled, orchestrator off), so Validate must never reject or warn
// regardless of Prompt/AgentsJSON content.
func TestValidateNoGatesActive(t *testing.T) {
	e := Env{DispatchKind: "work", BoxWriteEnabled: true, OrchestratorEnabled: false}
	result := Result{Prompt: "no markers anywhere", AgentsJSON: ""}

	warnings, err := Validate(e, result, testValidateMarkerRows(), nil)
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	if warnings != nil {
		t.Fatalf("Validate() warnings = %v, want nil", warnings)
	}
}

// TestValidateReadOnlyResearchReject covers the verdict-comment-relay row: a
// research + read-only dispatch whose rendered prompt is missing
// SPINDRIFT_COMMENT must reject.
func TestValidateReadOnlyResearchReject(t *testing.T) {
	e := Env{DispatchKind: "research", BoxWriteEnabled: false}
	result := Result{Prompt: "research stub, no verdict-comment marker here"}

	warnings, err := Validate(e, result, testValidateMarkerRows(), nil)
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil")
	}
	mustContain(t, err.Error(), "SPINDRIFT_COMMENT")
	if warnings != nil {
		t.Fatalf("Validate() warnings = %v, want nil", warnings)
	}
}

// TestValidateReadOnlyResearchPass covers the same gate as above, but with
// SPINDRIFT_COMMENT present -- no reject.
func TestValidateReadOnlyResearchPass(t *testing.T) {
	e := Env{DispatchKind: "research", BoxWriteEnabled: false}
	result := Result{Prompt: "research stub\n\nPost your verdict with SPINDRIFT_COMMENT here"}

	_, err := Validate(e, result, testValidateMarkerRows(), nil)
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

// TestValidateOrchestratorEnabledReject covers the reviewer-verdict row: the
// orchestrator on with a rendered review prompt missing VERDICT: must
// reject.
func TestValidateOrchestratorEnabledReject(t *testing.T) {
	e := Env{OrchestratorEnabled: true}
	result := Result{
		Handoff: Handoff{ReviewPromptFile: "reviewer stub, no verdict line here"},
	}

	warnings, err := Validate(e, result, testValidateMarkerRows(), nil)
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil")
	}
	mustContain(t, err.Error(), "VERDICT:")
	if warnings != nil {
		t.Fatalf("Validate() warnings = %v, want nil", warnings)
	}
}

// TestValidateOrchestratorEnabledNoFalsePositive covers the no-false-positive
// acceptance criterion (issue #2249 #3): when Handoff.ReviewPromptFile is
// empty (as when the orchestrator is off, or a research/fix-pass dispatch),
// the reviewer-verdict gate is never active regardless of content.
func TestValidateOrchestratorEnabledNoFalsePositive(t *testing.T) {
	e := Env{OrchestratorEnabled: true, BoxWriteEnabled: true}
	result := Result{Handoff: Handoff{ReviewPromptFile: ""}}

	warnings, err := Validate(e, result, testValidateMarkerRows(), nil)
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	if warnings != nil {
		t.Fatalf("Validate() warnings = %v, want nil", warnings)
	}
}

// TestValidateBoxAccessReadOnlyWarn covers the pr-intent row: read-only,
// non-research, prompt missing SPINDRIFT_PR_INTENT -- advisory only.
func TestValidateBoxAccessReadOnlyWarn(t *testing.T) {
	e := Env{DispatchKind: "work", BoxWriteEnabled: false}
	result := Result{Prompt: "issue stub, no PR-intent marker here"}

	warnings, err := Validate(e, result, testValidateMarkerRows(), nil)
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("Validate() warnings = %v, want exactly one entry", warnings)
	}
	mustContain(t, warnings[0], "SPINDRIFT_PR_INTENT")
}

// TestValidateFilerFileRelayWarn covers the issue-intent row: a filer-relay
// dispatch (filer configured, orchestrator on, read-only) whose filer prompt
// (extracted from AgentsJSON) is missing SPINDRIFT_ISSUE_INTENT -- advisory
// only.
func TestValidateFilerFileRelayWarn(t *testing.T) {
	e := Env{
		DispatchKind:        "work",
		BoxWriteEnabled:     false,
		OrchestratorEnabled: true,
		AgentsJSONTemplate:  `{"filer":{"model":"m"}}`,
	}
	result := Result{
		Prompt:     "issue stub",
		AgentsJSON: `{"filer":{"prompt":"no marker here"}}`,
	}

	warnings, err := Validate(e, result, testValidateMarkerRows(), nil)
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	mustContain(t, strings.Join(warnings, "\n"), "SPINDRIFT_ISSUE_INTENT")
}

// TestValidateDataDrivenSeverity is the data-driven proof (issue #2318):
// patching the pr-intent row's Severity to "reject" (with the same gate-
// active-marker-missing scenario TestValidateBoxAccessReadOnlyWarn exercises)
// must flip Validate's outcome to a reject, proving it dispatches on
// row.Severity/row.When data rather than a hardcoded per-id switch.
func TestValidateDataDrivenSeverity(t *testing.T) {
	e := Env{DispatchKind: "work", BoxWriteEnabled: false}
	result := Result{Prompt: "issue stub, no PR-intent marker here"}

	rows := testValidateMarkerRows()
	for i := range rows {
		if rows[i].ID == "pr-intent" {
			rows[i].Severity = "reject"
		}
	}

	warnings, err := Validate(e, result, rows, nil)
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil (severity patched to reject)")
	}
	mustContain(t, err.Error(), "SPINDRIFT_PR_INTENT")
	if warnings != nil {
		t.Fatalf("Validate() warnings = %v, want nil", warnings)
	}
}

// TestLoadValidateMarkersParsesAllRows round-trips testdata/validate-markers.json
// -- the hand transcription of lib/prompt-contract.nix's validateMarkers
// registry -- into []ValidateMarkerRow and asserts the decoded fields match.
func TestLoadValidateMarkersParsesAllRows(t *testing.T) {
	f, err := os.Open("testdata/validate-markers.json")
	if err != nil {
		t.Fatalf("open testdata/validate-markers.json: %v", err)
	}
	defer f.Close()

	rows, err := LoadValidateMarkers(f)
	if err != nil {
		t.Fatalf("LoadValidateMarkers: %v", err)
	}

	want := testValidateMarkerRows()
	if len(rows) != len(want) {
		t.Fatalf("len(rows) = %d, want %d", len(rows), len(want))
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("rows[%d] = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

// TestLoadValidateMarkersMalformed covers the error path: invalid JSON must
// return a non-nil, wrapped error, never panic.
func TestLoadValidateMarkersMalformed(t *testing.T) {
	f, err := os.Open("testdata/malformed.json")
	if err != nil {
		t.Fatalf("open testdata/malformed.json: %v", err)
	}
	defer f.Close()

	if _, err := LoadValidateMarkers(f); err == nil {
		t.Fatal("LoadValidateMarkers(malformed) = nil error, want non-nil")
	}
}

// TestLoadValidateMarkersFileMalformed exercises LoadValidateMarkersFile's
// own error path alongside LoadValidateMarkers's.
func TestLoadValidateMarkersFileMalformed(t *testing.T) {
	if _, err := LoadValidateMarkersFile("testdata/malformed.json"); err == nil {
		t.Fatal("LoadValidateMarkersFile(malformed) = nil error, want non-nil")
	}
}

// TestLoadValidateMarkersFileNonexistent covers a nonexistent path: a
// wrapped, non-nil error, never a panic.
func TestLoadValidateMarkersFileNonexistent(t *testing.T) {
	if _, err := LoadValidateMarkersFile("testdata/does-not-exist.json"); err == nil {
		t.Fatal("LoadValidateMarkersFile(nonexistent) = nil error, want non-nil")
	}
}

// TestValidateMarkerMessageVerbatim guards each row's pre-rendered Message
// field surfacing verbatim (byte-for-byte, marker already interpolated by
// the nix registry) as Validate's reject-error/warn-entry text -- the
// data-driven successor to the hardcoded per-When switch this test used to
// drive directly (issue #2318 parent; message text moved to the registry by
// #2405). Each case is a scenario tuned so exactly one row's gate is active
// with its marker missing, isolating that row's message in the outcome.
func TestValidateMarkerMessageVerbatim(t *testing.T) {
	rows := testValidateMarkerRows()
	rowMessage := func(id string) string {
		for _, r := range rows {
			if r.ID == id {
				return r.Message
			}
		}
		t.Fatalf("no row with id %q", id)
		return ""
	}

	t.Run("readOnlyResearch reject", func(t *testing.T) {
		e := Env{DispatchKind: "research", BoxWriteEnabled: false}
		result := Result{Prompt: "research stub, no verdict-comment marker here"}

		_, err := Validate(e, result, rows, nil)
		if err == nil {
			t.Fatal("Validate() error = nil, want non-nil")
		}
		want := rowMessage("verdict-comment-relay")
		if err.Error() != want {
			t.Errorf("Validate() error =\n%q\nwant\n%q", err.Error(), want)
		}
	})

	t.Run("orchestratorEnabled reject", func(t *testing.T) {
		e := Env{OrchestratorEnabled: true}
		result := Result{
			Handoff: Handoff{ReviewPromptFile: "reviewer stub, no verdict line here"},
		}

		_, err := Validate(e, result, rows, nil)
		if err == nil {
			t.Fatal("Validate() error = nil, want non-nil")
		}
		want := rowMessage("reviewer-verdict")
		if err.Error() != want {
			t.Errorf("Validate() error =\n%q\nwant\n%q", err.Error(), want)
		}
	})

	t.Run("boxAccessReadOnly warn", func(t *testing.T) {
		e := Env{DispatchKind: "work", BoxWriteEnabled: false}
		result := Result{Prompt: "issue stub, no PR-intent marker here"}

		warnings, err := Validate(e, result, rows, nil)
		if err != nil {
			t.Fatalf("Validate() error = %v, want nil", err)
		}
		if len(warnings) != 1 {
			t.Fatalf("Validate() warnings = %v, want exactly one entry", warnings)
		}
		want := rowMessage("pr-intent")
		if warnings[0] != want {
			t.Errorf("Validate() warnings[0] =\n%q\nwant\n%q", warnings[0], want)
		}
	})

	t.Run("filerFileRelay warn", func(t *testing.T) {
		e := Env{
			DispatchKind:        "work",
			BoxWriteEnabled:     false,
			OrchestratorEnabled: true,
			AgentsJSONTemplate:  `{"filer":{"model":"m"}}`,
		}
		result := Result{
			// Already carries SPINDRIFT_PR_INTENT so the boxAccessReadOnly
			// row's gate, also active under this Env, doesn't also warn --
			// isolates this case to the filerFileRelay row alone.
			Prompt:     "issue stub with SPINDRIFT_PR_INTENT already present",
			AgentsJSON: `{"filer":{"prompt":"no marker here"}}`,
		}

		warnings, err := Validate(e, result, rows, nil)
		if err != nil {
			t.Fatalf("Validate() error = %v, want nil", err)
		}
		if len(warnings) != 1 {
			t.Fatalf("Validate() warnings = %v, want exactly one entry", warnings)
		}
		want := rowMessage("issue-intent")
		if warnings[0] != want {
			t.Errorf("Validate() warnings[0] =\n%q\nwant\n%q", warnings[0], want)
		}
	})
}

// TestValidateForbiddenMarkerRejectsImperativeUnderActiveGate covers the
// forbiddenMarkers row wiring (issue #2464): a read-only, non-research
// dispatch whose rendered prompt orders `git push` as an un-negated
// numbered-list instruction must reject, with the forbidden-git-push row's
// own Message surfacing verbatim.
func TestValidateForbiddenMarkerRejectsImperativeUnderActiveGate(t *testing.T) {
	e := Env{BoxWriteEnabled: false, DispatchKind: "work"}
	result := Result{Prompt: "1. Push your branch with `git push` before you finish.\n"}

	_, err := Validate(e, result, nil, testForbiddenMarkerRows())
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil")
	}
	want := forbiddenMarkerMessage(t, "forbidden-git-push")
	if err.Error() != want {
		t.Errorf("Validate() error =\n%q\nwant\n%q", err.Error(), want)
	}
}

// TestValidateForbiddenMarkerPassesOnNegation covers the false-positive
// guard: the shipped if-blocked-push-outbox.md fragment's own negated
// `git push` list item (a read-only Box is explicitly told NOT to push) must
// never trip the forbidden-git-push row.
func TestValidateForbiddenMarkerPassesOnNegation(t *testing.T) {
	e := Env{BoxWriteEnabled: false, DispatchKind: "work"}
	result := Result{Prompt: "1. Your token is read-only and you take no code-out action yourself — do NOT\n" +
		"   `git push` and do NOT run `git bundle create` (or note if you have nothing\n" +
		"   committed to hand off). Leave what you have committed on the branch: after\n" +
		"   you exit the harness relays your committed branch out and the launcher\n" +
		"   pushes it host-side with its own token.\n"}

	_, err := Validate(e, result, nil, testForbiddenMarkerRows())
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

// TestValidateForbiddenMarkerInactiveUnderReadWrite covers the read-write
// path being unaffected: with BoxWriteEnabled true, the boxAccessReadOnly
// gate is never active, so an un-negated imperative `git push` in the
// rendered prompt is never even evaluated.
func TestValidateForbiddenMarkerInactiveUnderReadWrite(t *testing.T) {
	e := Env{BoxWriteEnabled: true}
	result := Result{Prompt: "1. Push your branch with `git push` before you finish.\n"}

	_, err := Validate(e, result, nil, testForbiddenMarkerRows())
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

// TestValidateForbiddenMarkerAbsentPasses covers the gate-active-but-marker-
// nowhere case: no forbidden marker text anywhere in the prompt is never a
// reject regardless of gate state.
func TestValidateForbiddenMarkerAbsentPasses(t *testing.T) {
	e := Env{BoxWriteEnabled: false, DispatchKind: "work"}
	result := Result{Prompt: "no forbidden markers anywhere in this prompt"}

	_, err := Validate(e, result, nil, testForbiddenMarkerRows())
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

// TestValidateForbiddenMarkerRejectsImperativeInFilerPrompt covers the
// filer-prompt haystack gap (issue #2464 follow-up): "gh issue create" only
// ever renders inside the filer's own rendered prompt (extracted from
// AgentsJSON via filerPromptFrom), never result.Prompt. A read-only,
// non-research dispatch whose filer prompt orders it as an un-negated
// numbered-list instruction must reject, even though result.Prompt itself
// carries nothing relevant.
func TestValidateForbiddenMarkerRejectsImperativeInFilerPrompt(t *testing.T) {
	e := Env{BoxWriteEnabled: false, DispatchKind: "work"}
	result := Result{
		Prompt:     "issue stub, nothing forbidden here",
		AgentsJSON: `{"filer":{"prompt":"1. File the issue via ` + "`gh issue create --title ...`" + ` before you finish.\n"}}`,
	}

	_, err := Validate(e, result, nil, testForbiddenMarkerRows())
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil")
	}
	want := forbiddenMarkerMessage(t, "forbidden-gh-issue-create")
	if err.Error() != want {
		t.Errorf("Validate() error =\n%q\nwant\n%q", err.Error(), want)
	}
}

// TestValidateForbiddenMarkerRejectsImperativeInReviewPromptFile covers the
// review-prompt-file haystack gap (issue #2464 follow-up), symmetric with
// the filer-prompt case above: a read-only, non-research dispatch whose
// orchestrator review prompt file orders "gh pr merge" as an un-negated
// numbered-list instruction must reject, even though result.Prompt itself
// carries nothing relevant.
func TestValidateForbiddenMarkerRejectsImperativeInReviewPromptFile(t *testing.T) {
	e := Env{BoxWriteEnabled: false, DispatchKind: "work"}
	result := Result{
		Prompt: "issue stub, nothing forbidden here",
		Handoff: Handoff{
			ReviewPromptFile: "1. Merge the PR via `gh pr merge` before you finish.\n",
		},
	}

	_, err := Validate(e, result, nil, testForbiddenMarkerRows())
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil")
	}
	want := forbiddenMarkerMessage(t, "forbidden-gh-pr-merge")
	if err.Error() != want {
		t.Errorf("Validate() error =\n%q\nwant\n%q", err.Error(), want)
	}
}

// TestValidateForbiddenMarkerToleratesGitForgeBranchUnderReadOnly covers
// liveCodeForge == "git" excluded from the whenBoxAccessReadOnly
// forbidden-row gate entirely (Validate's doc comment, validate.go): the
// shipped templates/default/prompts/issue-prompt.md's `**`CODE_FORGE=git`**`
// branch carries a genuine, ungated, un-negated numbered-list "git push"
// instruction -- correct, load-bearing content for that branch, never a
// drifted-fragment bug to catch. cmd/launcher/main.go's
// checkReadOnlyCapabilityGate separately refuses at launcher startup to
// ever dispatch BOX_FORGE_AND_ISSUE_ACCESS=read-only with CODE_FORGE=git,
// but this promptassembly-package Validate call has no such protection of
// its own -- entrypoint.sh's bats coverage exercises this exact combination
// directly (tests/entrypoint-pr-intent-nudge.bats's "PR-intent gate: never
// fires under CODE_FORGE=git"), so Validate must tolerate it.
func TestValidateForbiddenMarkerToleratesGitForgeBranchUnderReadOnly(t *testing.T) {
	e := Env{BoxWriteEnabled: false, DispatchKind: "work", CodeForge: "git"}
	result := Result{Prompt: "**`CODE_FORGE=git`** (push-only Code Forge — no PR, no CI-watch, no merge\n" +
		"gate): skip OPEN A PULL REQUEST below entirely.\n" +
		"\n" +
		"1. `git push --force-with-lease -u origin ${BRANCH}` (if not already pushed).\n" +
		"2. Print exactly one line as your final output and stop.\n"}

	_, err := Validate(e, result, nil, testForbiddenMarkerRows())
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

// TestValidateForbiddenMarkerGhAPIMutationKindNeverScannedAsSubstring covers
// issue #2499: the forbidden-gh-api-mutation row's Kind is
// "gh-api-mutation", meaning its Marker ("gh api") is display-only --
// lib/prompt-contract.nix's own row doc comment says enforcement is
// entrypoint.sh's install_readonly_gh_shim argument-scan (only a mutating
// `-X`/`--method` verb is rejected), not a plain-substring scan here. A
// read-only, non-research dispatch whose rendered prompt orders a plain,
// un-negated, read-only `gh api rate_limit` call (no mutating method flag)
// must never reject -- Validate must not treat "gh api" as a literal
// forbidden substring for this row.
func TestValidateForbiddenMarkerGhAPIMutationKindNeverScannedAsSubstring(t *testing.T) {
	e := Env{BoxWriteEnabled: false, DispatchKind: "work"}
	result := Result{Prompt: "1. Run `gh api rate_limit` to check your remaining quota before starting.\n"}

	_, err := Validate(e, result, nil, testForbiddenMarkerRows())
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

// TestValidateForbiddenMarkerFjRowStillRejectsImperative proves the
// gh-api-mutation Kind fix above is narrowly scoped: a "substring"-kind row
// (forbidden-fj-pr-create) must still reject an un-negated imperative
// `fj pr create` exactly as before.
func TestValidateForbiddenMarkerFjRowStillRejectsImperative(t *testing.T) {
	e := Env{BoxWriteEnabled: false, DispatchKind: "work"}
	result := Result{Prompt: "1. Open the PR with `fj pr create` before you finish.\n"}

	_, err := Validate(e, result, nil, testForbiddenMarkerRows())
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil")
	}
	want := forbiddenMarkerMessage(t, "forbidden-fj-pr-create")
	if err.Error() != want {
		t.Errorf("Validate() error =\n%q\nwant\n%q", err.Error(), want)
	}
}

// forbiddenMarkerMessage returns testForbiddenMarkerRows()'s Message field
// for the row with the given id, failing the test if no such row exists.
func forbiddenMarkerMessage(t *testing.T, id string) string {
	t.Helper()
	for _, r := range testForbiddenMarkerRows() {
		if r.ID == id {
			return r.Message
		}
	}
	t.Fatalf("no forbidden marker row with id %q", id)
	return ""
}

// testValidateMarkerRows returns the four validateMarkers rows in
// lib/prompt-contract.nix's own order, for tests that don't need to load
// them from testdata/validate-markers.json.
func testValidateMarkerRows() []ValidateMarkerRow {
	return []ValidateMarkerRow{
		{
			ID:       "verdict-comment-relay",
			Marker:   "SPINDRIFT_COMMENT",
			Carrier:  "fragment-body",
			Severity: "reject",
			When:     "readOnlyResearch",
			Message:  "_validate_prompt_contract: read-only research dispatch's rendered prompt is missing the required 'SPINDRIFT_COMMENT' marker -- this belongs in research-prompt.md's (or a SPINDRIFT_PROMPT_DIR override's) POST THE VERDICT section; without it a read-only Box has no way to hand its verdict to the launcher. Refusing to invoke the Driver.",
		},
		{
			ID:       "reviewer-verdict",
			Marker:   "VERDICT:",
			Carrier:  "subagent-first-line",
			Severity: "reject",
			When:     "orchestratorEnabled",
			Message:  "_validate_prompt_contract: the orchestrator's rendered review prompt is missing the required 'VERDICT:' marker -- this belongs in review-prompt.md's (or a SPINDRIFT_PROMPT_DIR override's) verdict line; without it the code-owned review loop has nothing to gate on. Refusing to invoke the Driver.",
		},
		{
			ID:       "pr-intent",
			Marker:   "SPINDRIFT_PR_INTENT",
			Carrier:  "fragment-body",
			Severity: "warn",
			When:     "boxAccessReadOnly",
			Message:  "_validate_prompt_contract: warning -- read-only dispatch's rendered prompt is missing the 'SPINDRIFT_PR_INTENT' marker (belongs in issue-prompt.md's, or fix-prompt.md's injected, OPEN A PULL REQUEST section). Proceeding: a status=ready run with no PR-intent line still gets one resume-nudge attempt post-driver, and a genuinely exhausted attempt falls back to the merge-blocked report rather than losing the branch.",
		},
		{
			ID:       "issue-intent",
			Marker:   "SPINDRIFT_ISSUE_INTENT",
			Carrier:  "fragment-body",
			Severity: "warn",
			When:     "filerFileRelay",
			Message:  "_validate_prompt_contract: warning -- filer-relay dispatch's rendered filer prompt is missing the 'SPINDRIFT_ISSUE_INTENT' marker (belongs in filer-prompt.md's, or a SPINDRIFT_PROMPT_DIR override's, filer-file-relay-injected section). Proceeding: the filer's own best-effort PR-body fallback still records the issue reference even without the relay.",
		},
	}
}

// mustContain is a small helper asserting substr appears in s; the marker
// alone suffices for the gate-logic tests above, since
// TestValidateMarkerMessageVerbatim separately guards each row's exact
// message text against the registry's Message field.
func mustContain(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Fatalf("%q does not contain %q", s, substr)
	}
}
