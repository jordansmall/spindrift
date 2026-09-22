package promptassembly

import (
	"os"
	"strings"
	"testing"
)

// Every gate the validateMarkers rows key off of is off here, so Validate must
// neither reject nor warn whatever the Prompt and AgentsJSON contain.
func TestValidateNoGatesActive(t *testing.T) {
	e := Env{DispatchKind: "work", BoxWriteEnabled: true, OrchestratorEnabled: false}
	result := Result{Prompt: "no markers anywhere", AgentsJSON: ""}

	warnings, err := Validate(e, result, testValidateMarkerRows())
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	if warnings != nil {
		t.Fatalf("Validate() warnings = %v, want nil", warnings)
	}
}

// Pins the verdict-comment-relay row: a read-only research dispatch whose
// rendered prompt lacks SPINDRIFT_COMMENT must reject.
func TestValidateReadOnlyResearchReject(t *testing.T) {
	e := Env{DispatchKind: "research", BoxWriteEnabled: false}
	result := Result{Prompt: "research stub, no verdict-comment marker here"}

	warnings, err := Validate(e, result, testValidateMarkerRows())
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil")
	}
	mustContain(t, err.Error(), "SPINDRIFT_COMMENT")
	if warnings != nil {
		t.Fatalf("Validate() warnings = %v, want nil", warnings)
	}
}

// The verdict-comment-relay row's no-false-positive case.
func TestValidateReadOnlyResearchPass(t *testing.T) {
	e := Env{DispatchKind: "research", BoxWriteEnabled: false}
	result := Result{Prompt: "research stub\n\nPost your verdict with SPINDRIFT_COMMENT here"}

	_, err := Validate(e, result, testValidateMarkerRows())
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

// The verdict-comment-relay row's other trigger (issue #2593): a research
// dispatch with the Filer provisioned forces FILER_FILE_RELAY even when
// BoxWriteEnabled is true, per gates_tracker.go's researchForceRelay. A
// missing SPINDRIFT_COMMENT there loses the verdict unrecoverably, so this
// rejects even though BOX_ACCESS_READ_ONLY is false.
func TestValidateReadWriteResearchFilerRelayReject(t *testing.T) {
	e := Env{DispatchKind: "research", BoxWriteEnabled: true, FilerEnabled: true}
	result := Result{Prompt: "research stub, no verdict-comment marker here"}

	warnings, err := Validate(e, result, testValidateMarkerRows())
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil")
	}
	mustContain(t, err.Error(), "SPINDRIFT_COMMENT")
	if warnings != nil {
		t.Fatalf("Validate() warnings = %v, want nil", warnings)
	}
}

// The issue #2593 gate's no-false-positive case.
func TestValidateReadWriteResearchFilerRelayPass(t *testing.T) {
	e := Env{DispatchKind: "research", BoxWriteEnabled: true, FilerEnabled: true}
	result := Result{Prompt: "research stub\n\nPost your verdict with SPINDRIFT_COMMENT here"}

	_, err := Validate(e, result, testValidateMarkerRows())
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

// Pins the reviewer-verdict row: the orchestrator on with a rendered review
// prompt missing VERDICT: must reject.
func TestValidateOrchestratorEnabledReject(t *testing.T) {
	e := Env{OrchestratorEnabled: true}
	result := Result{
		ReviewPromptText: "reviewer stub, no verdict line here",
	}

	warnings, err := Validate(e, result, testValidateMarkerRows())
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil")
	}
	mustContain(t, err.Error(), "VERDICT:")
	if warnings != nil {
		t.Fatalf("Validate() warnings = %v, want nil", warnings)
	}
}

// Acceptance criterion 3 of issue #2249, no false positives: an empty
// Result.ReviewPromptText (a research or fix-pass dispatch) leaves the
// reviewer-verdict gate inactive whatever the content.
func TestValidateOrchestratorEnabledNoFalsePositive(t *testing.T) {
	e := Env{OrchestratorEnabled: true, BoxWriteEnabled: true}
	result := Result{ReviewPromptText: ""}

	warnings, err := Validate(e, result, testValidateMarkerRows())
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	if warnings != nil {
		t.Fatalf("Validate() warnings = %v, want nil", warnings)
	}
}

// Pins the pr-intent row: a read-only, non-research prompt missing
// SPINDRIFT_PR_INTENT warns rather than rejects.
func TestValidateBoxAccessReadOnlyWarn(t *testing.T) {
	e := Env{DispatchKind: "work", BoxWriteEnabled: false}
	result := Result{Prompt: "issue stub, no PR-intent marker here"}

	warnings, err := Validate(e, result, testValidateMarkerRows())
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("Validate() warnings = %v, want exactly one entry", warnings)
	}
	mustContain(t, warnings[0], "SPINDRIFT_PR_INTENT")
}

// Pins the issue-intent row, which scans the filer prompt extracted from
// AgentsJSON rather than result.Prompt, and warns rather than rejects.
func TestValidateFilerFileRelayWarn(t *testing.T) {
	e := Env{
		DispatchKind:        "work",
		BoxWriteEnabled:     false,
		OrchestratorEnabled: true,
		AgentsJSONTemplate:  `{"filer":{"model":"m"}}`,
		FilerEnabled:        true,
	}
	result := Result{
		Prompt:     "issue stub",
		AgentsJSON: `{"filer":{"prompt":"no marker here"}}`,
	}

	warnings, err := Validate(e, result, testValidateMarkerRows())
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	mustContain(t, strings.Join(warnings, "\n"), "SPINDRIFT_ISSUE_INTENT")
}

// Pins the research-issue-intent row (issue #2593), which scans result.Prompt,
// not the filer's. The fixture is tuned so only that row fires: filer.prompt
// already carries SPINDRIFT_ISSUE_INTENT to keep the issue-intent row quiet,
// and Prompt already carries SPINDRIFT_COMMENT so verdict-comment-relay does
// not reject first.
func TestValidateResearchFileRelayWarn(t *testing.T) {
	e := Env{DispatchKind: "research", FilerEnabled: true, BoxWriteEnabled: true}
	result := Result{
		Prompt:     "research stub with SPINDRIFT_COMMENT present, no issue-intent marker here",
		AgentsJSON: `{"filer":{"prompt":"already has SPINDRIFT_ISSUE_INTENT"}}`,
	}

	warnings, err := Validate(e, result, testValidateMarkerRows())
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("Validate() warnings = %v, want exactly one entry", warnings)
	}
	mustContain(t, warnings[0], "SPINDRIFT_ISSUE_INTENT")
}

// The research-issue-intent row's no-false-positive case, with the same
// isolating fixture as TestValidateResearchFileRelayWarn: filer.prompt carries
// the marker so the issue-intent row stays quiet, and Prompt carries
// SPINDRIFT_COMMENT so verdict-comment-relay does not reject first.
func TestValidateResearchFileRelayNoFalsePositiveMarkerPresent(t *testing.T) {
	e := Env{DispatchKind: "research", FilerEnabled: true, BoxWriteEnabled: true}
	result := Result{
		Prompt:     "research stub ... SPINDRIFT_ISSUE_INTENT ... SPINDRIFT_COMMENT ... present",
		AgentsJSON: `{"filer":{"prompt":"already has SPINDRIFT_ISSUE_INTENT"}}`,
	}

	warnings, err := Validate(e, result, testValidateMarkerRows())
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	if warnings != nil {
		t.Fatalf("Validate() warnings = %v, want nil", warnings)
	}
}

// A work-kind dispatch leaves the research-issue-intent gate inactive however
// FilerEnabled is set, so a missing marker draws no warning.
func TestValidateResearchFileRelayNoFalsePositiveGateOff(t *testing.T) {
	e := Env{DispatchKind: "work", FilerEnabled: true, BoxWriteEnabled: true}
	result := Result{Prompt: "issue stub, no issue-intent marker here"}

	warnings, err := Validate(e, result, testValidateMarkerRows())
	if err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	if warnings != nil {
		t.Fatalf("Validate() warnings = %v, want nil", warnings)
	}
}

// Issue #2318: patching the pr-intent row's Severity to "reject" must flip
// Validate's outcome, proving it dispatches on the row's Severity and When
// data rather than a hardcoded per-id switch.
func TestValidateDataDrivenSeverity(t *testing.T) {
	e := Env{DispatchKind: "work", BoxWriteEnabled: false}
	result := Result{Prompt: "issue stub, no PR-intent marker here"}

	rows := testValidateMarkerRows()
	for i := range rows {
		if rows[i].ID == "pr-intent" {
			rows[i].Severity = "reject"
		}
	}

	warnings, err := Validate(e, result, rows)
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil (severity patched to reject)")
	}
	mustContain(t, err.Error(), "SPINDRIFT_PR_INTENT")
	if warnings != nil {
		t.Fatalf("Validate() warnings = %v, want nil", warnings)
	}
}

// testdata/validate-markers.json is a hand transcription of
// lib/prompt-contract.nix's validateMarkers registry, so this round trip is
// what catches the two drifting apart.
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

func TestLoadValidateMarkersFileMalformed(t *testing.T) {
	if _, err := LoadValidateMarkersFile("testdata/malformed.json"); err == nil {
		t.Fatal("LoadValidateMarkersFile(malformed) = nil error, want non-nil")
	}
}

func TestLoadValidateMarkersFileNonexistent(t *testing.T) {
	if _, err := LoadValidateMarkersFile("testdata/does-not-exist.json"); err == nil {
		t.Fatal("LoadValidateMarkersFile(nonexistent) = nil error, want non-nil")
	}
}

// Each row's pre-rendered Message must reach Validate's reject error or warn
// entry byte for byte, with the marker already interpolated by the nix
// registry (issue #2318; message text moved to the registry by #2405). Every
// case is tuned so exactly one row's gate is active with its marker missing,
// which isolates that row's message in the outcome.
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

		_, err := Validate(e, result, rows)
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
			ReviewPromptText: "reviewer stub, no verdict line here",
		}

		_, err := Validate(e, result, rows)
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

		warnings, err := Validate(e, result, rows)
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
			FilerEnabled:        true,
		}
		result := Result{
			// Carries SPINDRIFT_PR_INTENT so the boxAccessReadOnly row, whose
			// gate is also active here, stays quiet and leaves only the
			// filerFileRelay row's warning.
			Prompt:     "issue stub with SPINDRIFT_PR_INTENT already present",
			AgentsJSON: `{"filer":{"prompt":"no marker here"}}`,
		}

		warnings, err := Validate(e, result, rows)
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

// Issue #3726: under BOX_SIGNAL_CARRIER=socket, verdict-comment-relay scans
// for the verb (driver-exec signal comment) instead of the SPINDRIFT_COMMENT
// marker, and log mode (the default, empty SignalCarrier) is unaffected.
func TestValidateVerdictCommentRelaySignalCarrier(t *testing.T) {
	rows := testValidateMarkerRows()
	baseEnv := Env{DispatchKind: "research", BoxWriteEnabled: false}

	t.Run("socket mode rejects on the marker alone", func(t *testing.T) {
		e := baseEnv
		e.SignalCarrier = "socket"
		result := Result{Prompt: "research stub, SPINDRIFT_COMMENT present but no verb"}

		_, err := Validate(e, result, rows)
		if err == nil {
			t.Fatal("Validate() error = nil, want non-nil")
		}
		mustContain(t, err.Error(), "driver-exec signal comment")
	})

	t.Run("socket mode passes on the verb", func(t *testing.T) {
		e := baseEnv
		e.SignalCarrier = "socket"
		result := Result{Prompt: "research stub, driver-exec signal comment -body-file verdict.md"}

		_, err := Validate(e, result, rows)
		if err != nil {
			t.Fatalf("Validate() error = %v, want nil", err)
		}
	})

	t.Run("log mode rejects on the verb alone", func(t *testing.T) {
		e := baseEnv
		result := Result{Prompt: "research stub, driver-exec signal comment -body-file verdict.md"}

		_, err := Validate(e, result, rows)
		if err == nil {
			t.Fatal("Validate() error = nil, want non-nil")
		}
		mustContain(t, err.Error(), "SPINDRIFT_COMMENT")
	})
}

// Issue #3726: pr-intent's socket/log split, the warn-severity counterpart of
// TestValidateVerdictCommentRelaySignalCarrier.
func TestValidatePrIntentSignalCarrier(t *testing.T) {
	rows := testValidateMarkerRows()
	baseEnv := Env{DispatchKind: "work", BoxWriteEnabled: false}

	t.Run("socket mode warns on the marker alone", func(t *testing.T) {
		e := baseEnv
		e.SignalCarrier = "socket"
		result := Result{Prompt: "issue stub, SPINDRIFT_PR_INTENT present but no verb"}

		warnings, err := Validate(e, result, rows)
		if err != nil {
			t.Fatalf("Validate() error = %v, want nil", err)
		}
		if len(warnings) != 1 {
			t.Fatalf("Validate() warnings = %v, want exactly one entry", warnings)
		}
		mustContain(t, warnings[0], "driver-exec signal pr-intent")
	})

	t.Run("socket mode passes on the verb", func(t *testing.T) {
		e := baseEnv
		e.SignalCarrier = "socket"
		result := Result{Prompt: `issue stub, driver-exec signal pr-intent -title "x"`}

		warnings, err := Validate(e, result, rows)
		if err != nil {
			t.Fatalf("Validate() error = %v, want nil", err)
		}
		if warnings != nil {
			t.Fatalf("Validate() warnings = %v, want nil", warnings)
		}
	})

	t.Run("log mode warns on the verb alone", func(t *testing.T) {
		e := baseEnv
		result := Result{Prompt: `issue stub, driver-exec signal pr-intent -title "x"`}

		warnings, err := Validate(e, result, rows)
		if err != nil {
			t.Fatalf("Validate() error = %v, want nil", err)
		}
		if len(warnings) != 1 {
			t.Fatalf("Validate() warnings = %v, want exactly one entry", warnings)
		}
		mustContain(t, warnings[0], "SPINDRIFT_PR_INTENT")
	})
}

// Issue #3726: issue-intent's socket/log split. Its haystack is the filer
// prompt extracted from AgentsJSON, not result.Prompt, so Prompt here always
// carries both forms of the sibling pr-intent row's requirement (boxAccessReadOnly
// is active too under this Env) to keep that row quiet and isolate the
// assertion to issue-intent's own warning.
func TestValidateIssueIntentSignalCarrier(t *testing.T) {
	rows := testValidateMarkerRows()
	const promptKeepsPrIntentQuiet = "issue stub already ran driver-exec signal pr-intent and logged SPINDRIFT_PR_INTENT"
	baseEnv := Env{
		DispatchKind:        "work",
		BoxWriteEnabled:     false,
		OrchestratorEnabled: true,
		AgentsJSONTemplate:  `{"filer":{"model":"m"}}`,
		FilerEnabled:        true,
	}

	t.Run("socket mode warns on the marker alone", func(t *testing.T) {
		e := baseEnv
		e.SignalCarrier = "socket"
		result := Result{
			Prompt:     promptKeepsPrIntentQuiet,
			AgentsJSON: `{"filer":{"prompt":"SPINDRIFT_ISSUE_INTENT marker, no verb"}}`,
		}

		warnings, err := Validate(e, result, rows)
		if err != nil {
			t.Fatalf("Validate() error = %v, want nil", err)
		}
		if len(warnings) != 1 {
			t.Fatalf("Validate() warnings = %v, want exactly one entry", warnings)
		}
		mustContain(t, warnings[0], "driver-exec signal issue-intent")
	})

	t.Run("socket mode passes on the verb", func(t *testing.T) {
		e := baseEnv
		e.SignalCarrier = "socket"
		result := Result{
			Prompt:     promptKeepsPrIntentQuiet,
			AgentsJSON: `{"filer":{"prompt":"driver-exec signal issue-intent -title \"x\" -type bug"}}`,
		}

		warnings, err := Validate(e, result, rows)
		if err != nil {
			t.Fatalf("Validate() error = %v, want nil", err)
		}
		if warnings != nil {
			t.Fatalf("Validate() warnings = %v, want nil", warnings)
		}
	})

	t.Run("log mode warns on the verb alone", func(t *testing.T) {
		e := baseEnv
		result := Result{
			Prompt:     promptKeepsPrIntentQuiet,
			AgentsJSON: `{"filer":{"prompt":"driver-exec signal issue-intent -title \"x\" -type bug"}}`,
		}

		warnings, err := Validate(e, result, rows)
		if err != nil {
			t.Fatalf("Validate() error = %v, want nil", err)
		}
		if len(warnings) != 1 {
			t.Fatalf("Validate() warnings = %v, want exactly one entry", warnings)
		}
		mustContain(t, warnings[0], "SPINDRIFT_ISSUE_INTENT")
	})
}

// Issue #3726: research-issue-intent's socket/log split. Its gate
// (researchFileRelay) forces two sibling rows active too --
// verdict-comment-relay (readOnlyResearch) and issue-intent (filerFileRelay)
// -- so every fixture keeps both quiet in both carrier modes by carrying both
// their marker and their verb.
func TestValidateResearchIssueIntentSignalCarrier(t *testing.T) {
	rows := testValidateMarkerRows()
	const (
		commentQuiet     = "already ran driver-exec signal comment and logged SPINDRIFT_COMMENT"
		filerPromptQuiet = `{"filer":{"prompt":"already ran driver-exec signal issue-intent and logged SPINDRIFT_ISSUE_INTENT"}}`
	)
	baseEnv := Env{DispatchKind: "research", FilerEnabled: true, BoxWriteEnabled: true}

	t.Run("socket mode warns on the marker alone", func(t *testing.T) {
		e := baseEnv
		e.SignalCarrier = "socket"
		result := Result{
			Prompt:     "research stub, " + commentQuiet + ", SPINDRIFT_ISSUE_INTENT present but no verb",
			AgentsJSON: filerPromptQuiet,
		}

		warnings, err := Validate(e, result, rows)
		if err != nil {
			t.Fatalf("Validate() error = %v, want nil", err)
		}
		if len(warnings) != 1 {
			t.Fatalf("Validate() warnings = %v, want exactly one entry", warnings)
		}
		mustContain(t, warnings[0], "driver-exec signal issue-intent")
	})

	t.Run("socket mode passes on the verb", func(t *testing.T) {
		e := baseEnv
		e.SignalCarrier = "socket"
		result := Result{
			Prompt:     "research stub, " + commentQuiet + `, driver-exec signal issue-intent -title "x" -type bug`,
			AgentsJSON: filerPromptQuiet,
		}

		warnings, err := Validate(e, result, rows)
		if err != nil {
			t.Fatalf("Validate() error = %v, want nil", err)
		}
		if warnings != nil {
			t.Fatalf("Validate() warnings = %v, want nil", warnings)
		}
	})

	t.Run("log mode warns on the verb alone", func(t *testing.T) {
		e := baseEnv
		result := Result{
			Prompt:     "research stub, " + commentQuiet + `, driver-exec signal issue-intent -title "x" -type bug`,
			AgentsJSON: filerPromptQuiet,
		}

		warnings, err := Validate(e, result, rows)
		if err != nil {
			t.Fatalf("Validate() error = %v, want nil", err)
		}
		if len(warnings) != 1 {
			t.Fatalf("Validate() warnings = %v, want exactly one entry", warnings)
		}
		mustContain(t, warnings[0], "SPINDRIFT_ISSUE_INTENT")
	})
}

// Issue #3726: reviewer-verdict carries no SocketMarker -- VERDICT: is not a
// signal channel and never crosses the Signal socket -- so
// SIGNAL_CARRIER_SOCKET must never change its gate: it scans for VERDICT: in
// both carrier modes.
func TestValidateReviewerVerdictCarrierBlind(t *testing.T) {
	rows := testValidateMarkerRows()

	for _, carrier := range []string{"", "socket"} {
		e := Env{OrchestratorEnabled: true, SignalCarrier: carrier}
		result := Result{ReviewPromptText: "reviewer stub, no verdict line here"}

		_, err := Validate(e, result, rows)
		if err == nil {
			t.Fatalf("carrier=%q: Validate() error = nil, want non-nil", carrier)
		}
		mustContain(t, err.Error(), "VERDICT:")
	}
}

// The rows stay in lib/prompt-contract.nix's own order, because
// TestLoadValidateMarkersParsesAllRows compares them against the testdata file
// index by index.
func testValidateMarkerRows() []ValidateMarkerRow {
	return []ValidateMarkerRow{
		{
			ID:            "verdict-comment-relay",
			Marker:        "SPINDRIFT_COMMENT",
			Carrier:       "fragment-body",
			Severity:      "reject",
			When:          "readOnlyResearch",
			Message:       "_validate_prompt_contract: read-only research dispatch's rendered prompt is missing the required 'SPINDRIFT_COMMENT' marker -- this belongs in research-prompt.md's (or a SPINDRIFT_PROMPT_DIR override's) POST THE VERDICT section; without it a read-only Box has no way to hand its verdict to the launcher. Refusing to invoke the Driver.",
			SocketMarker:  "driver-exec signal comment",
			SocketMessage: "_validate_prompt_contract: read-only research dispatch's rendered prompt is missing the required 'driver-exec signal comment' call -- this belongs in research-prompt.md's (or a SPINDRIFT_PROMPT_DIR override's) POST THE VERDICT section; without it a read-only Box has no way to hand its verdict to the launcher. Refusing to invoke the Driver.",
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
			ID:            "pr-intent",
			Marker:        "SPINDRIFT_PR_INTENT",
			Carrier:       "fragment-body",
			Severity:      "warn",
			When:          "boxAccessReadOnly",
			Message:       "_validate_prompt_contract: warning -- read-only dispatch's rendered prompt is missing the 'SPINDRIFT_PR_INTENT' marker (belongs in issue-prompt.md's, or fix-prompt.md's injected, OPEN A PULL REQUEST section). Proceeding: a status=ready run with no PR-intent line still gets one resume-nudge attempt post-driver, and a genuinely exhausted attempt falls back to the merge-blocked report rather than losing the branch.",
			SocketMarker:  "driver-exec signal pr-intent",
			SocketMessage: "_validate_prompt_contract: warning -- read-only dispatch's rendered prompt is missing the 'driver-exec signal pr-intent' call (belongs in issue-prompt.md's, or fix-prompt.md's injected, OPEN A PULL REQUEST section). Proceeding: a status=ready run with no PR-intent line still gets one resume-nudge attempt post-driver, and a genuinely exhausted attempt falls back to the merge-blocked report rather than losing the branch.",
		},
		{
			ID:            "issue-intent",
			Marker:        "SPINDRIFT_ISSUE_INTENT",
			Carrier:       "fragment-body",
			Severity:      "warn",
			When:          "filerFileRelay",
			Message:       "_validate_prompt_contract: warning -- filer-relay dispatch's rendered filer prompt is missing the 'SPINDRIFT_ISSUE_INTENT' marker (belongs in filer-prompt.md's, or a SPINDRIFT_PROMPT_DIR override's, filer-file-relay-injected section). Proceeding: the filer's own best-effort PR-body fallback still records the issue reference even without the relay.",
			SocketMarker:  "driver-exec signal issue-intent",
			SocketMessage: "_validate_prompt_contract: warning -- filer-relay dispatch's rendered filer prompt is missing the 'driver-exec signal issue-intent' call (belongs in filer-prompt.md's, or a SPINDRIFT_PROMPT_DIR override's, filer-file-relay-injected section). Proceeding: the filer's own best-effort PR-body fallback still records the issue reference even without the relay.",
		},
		{
			ID:            "research-issue-intent",
			Marker:        "SPINDRIFT_ISSUE_INTENT",
			Carrier:       "fragment-body",
			Severity:      "warn",
			When:          "researchFileRelay",
			Message:       "_validate_prompt_contract: warning -- research dispatch's rendered prompt is missing the 'SPINDRIFT_ISSUE_INTENT' marker under an active Filer-relay gate (belongs in research-prompt.md's, or research-self-contained-prompt.md's, POST THE VERDICT section, research-file-issues-relay.md-substituted). Proceeding: any finding the filer can't relay still surfaces inline via its own best-effort fallback (describe it directly in the verdict body), and the researcher's posted verdict comment is unaffected either way.",
			SocketMarker:  "driver-exec signal issue-intent",
			SocketMessage: "_validate_prompt_contract: warning -- research dispatch's rendered prompt is missing the 'driver-exec signal issue-intent' call under an active Filer-relay gate (belongs in research-prompt.md's, or research-self-contained-prompt.md's, POST THE VERDICT section, research-file-issues-relay.md-substituted). Proceeding: any finding the filer can't relay still surfaces inline via its own best-effort fallback (describe it directly in the verdict body), and the researcher's posted verdict comment is unaffected either way.",
		},
	}
}

// The gate-logic tests match on the marker alone, because
// TestValidateMarkerMessageVerbatim separately guards each row's exact message
// text against the registry's Message field.
func mustContain(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Fatalf("%q does not contain %q", s, substr)
	}
}
