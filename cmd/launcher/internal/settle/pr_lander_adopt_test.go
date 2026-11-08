package settle

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

const adoptIntent = "feat: add widget\n\nAdds a widget."

// boxRun is a read-only Box's outcome as the adoption fingerprint reads it:
// whether an outcome line was found at all, its provenance, the driver's
// self-reported status ("" when it never self-reported), and the PR-intent
// body ("" when the log carried none).
func boxRun(found bool, prov outcome.Provenance, selfReport, intent string) *dispatch.Result {
	r := &dispatch.Result{PRIntent: intent, PRIntentFound: intent != ""}
	if found {
		r.Resolved.Found, r.Resolved.Provenance = true, prov
		r.Resolved.Outcome = outcome.Outcome{
			Issue: "1", Landing: handoffBranch, Status: "blocked",
			Synthetic: prov == outcome.ProvenanceSynthetic, Note: "driver exited without emitting an outcome",
		}
	}
	if selfReport != "" {
		r.Resolved.SelfReportFound, r.Resolved.SelfReport = true, outcome.SelfReport{Status: selfReport}
	}
	return r
}

// relayedBundle checks the Box's bundle was relayed in once, onto the agent branch.
func (g *handoffRig) relayedBundle() checkFn {
	return func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
		t.Helper()
		want := forge.RelayBundleCall{OutboxDir: g.outbox, Ref: handoffBranch}
		if len(fc.RelayBundleCalls) != 1 || fc.RelayBundleCalls[0] != want {
			t.Errorf("RelayBundleCalls = %+v, want exactly [%+v]", fc.RelayBundleCalls, want)
		}
	}
}

// openedDraftPR checks the single draft PR the host opened.
func openedDraftPR(title, body string) checkFn {
	return func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
		t.Helper()
		want := forge.CreateDraftPRCall{Title: title, Body: body, Base: "main", Head: handoffBranch}
		if len(fc.CreateDraftPRCalls) != 1 || fc.CreateDraftPRCalls[0] != want {
			t.Errorf("CreateDraftPRCalls = %+v, want exactly [%+v]", fc.CreateDraftPRCalls, want)
		}
	}
}

// adoptCases are the rows where Settle, or Recover's relay arm,
// adopts a finished branch it relays in and opens a PR for although the Box's
// own outcome line did not say ready (issues #2224, #2253, #2225): the
// fingerprint that licenses the adoption, and each leg whose absence refuses it.
func adoptCases() []prCase {
	g := &handoffRig{}
	failed := []string{"agent-failed"}
	flipThenMerge := []string{"MarkReady:" + testPR, "Merge:" + testPR}
	errBundleMissing := errors.New("bundle missing")
	errDraft500 := errors.New("create draft PR: 500")
	ready := outcome.StatusReady
	synthetic, genuine := outcome.ProvenanceSynthetic, outcome.ProvenanceGenuine
	readOnly := g.readOnly(0, nil)
	closes := "\n\nCloses #1"
	registered := []forge.RollupState{forge.StatePending, green, green}
	parked := &dispatch.Result{}
	boxBlocked := Settlement{Verdict: ForReason(dispatchrecord.ReasonBlocked), Note: "driver exited without emitting an outcome"}
	noOutcome := Settlement{Verdict: ForReason(dispatchrecord.ReasonNoOutcome), Note: "no outcome in log  class=  reason="}
	noteComment := []string{"driver exited without emitting an outcome"}

	adopts := func(c prCase) prCase {
		if c.shape == shapeGithub {
			c.shape = shapeGithubReadOnly
		}
		c.noPR = true
		if c.want == (Settlement{}) {
			c.want, c.wantLabels, c.wantNoLabels, c.wantMerged, c.wantCalls = merged, complete, failed, testPR, flipThenMerge
		}
		if c.setup == nil {
			c.setup = readOnly
		}
		return c
	}
	// refuses is a Settle that adopts nothing: no draft PR, no merge, the issue parked failed.
	refuses := func(c prCase) prCase {
		c.shape, c.noPR = shapeGithubReadOnly, true
		if c.setup == nil {
			c.setup = readOnly
		}
		c.wantPRClosed, c.wantLabels, c.wantNoLabels = true, failed, complete
		c.check = allOf(c.check, draftPRCalls(0))
		return c
	}
	adoptedNote := func(note string) []Step {
		return []Step{stepAt(testPR, StatusAdopted, "note="+note)}
	}
	// declines is Recover's relay arm handing the issue back untouched to its caller.
	declines := func(c prCase) prCase {
		c.shape, c.noPR, c.entry, c.wantAbandoned = shapeGithubReadOnly, true, entryRelayed, true
		if c.setup == nil {
			c.setup = readOnly
		}
		return c
	}

	return []prCase{
		// Issue #2475: an adopted PR whose rollup reads SUCCESS on every poll,
		// with no PENDING ever proving this run's own checks registered, still
		// merges once the bounded registration window (3 polls) elapses.
		{
			name: "adopted: a stale SUCCESS rollup merges once the registration window elapses", entry: entryAdopted,
			wantStatuses: []Status{StatusAdopted, StatusVerifiedMerged},
			checks:       []forge.RollupState{green, green, green, green, green},
			setup:        func(_ *testing.T, _ *forge.Fake, c *Config) { c.MergePollTimeout = 10 },
			want:         merged, wantLabels: complete, wantNoLabels: failed, wantMerged: testPR, wantCalls: flipThenMerge,
		},
		// The counterpart: a deadline shorter than the window times out and demotes.
		{
			name: "adopted: a stale SUCCESS rollup that times out within the window fails with the guard's reason", entry: entryAdopted,
			wantStatuses: []Status{StatusAdopted, StatusGateTerminal, StatusFailed},
			checks:       []forge.RollupState{green, green},
			setup:        func(_ *testing.T, _ *forge.Fake, c *Config) { c.MergePollTimeout = 1 },
			want:         Settlement{Verdict: ForReason(dispatchrecord.ReasonGateTerminal), PRURL: testPR, Note: "ci-timeout: registration guard never cleared after 1s"},
			wantPRClosed: true, wantLabels: failed, wantNoLabels: complete,
			wantComments: []string{"registration guard never cleared"},
		},
		adopts(prCase{
			name:         "a synthetic blocked outcome with a success self-report is adopted from the box's PR intent",
			wantStatuses: []Status{detailOnly, StatusAdopted, StatusVerifiedMerged},
			result:       boxRun(true, synthetic, ready, adoptIntent),
			check:        allOf(g.relayedBundle(), openedDraftPR("feat: add widget", "Adds a widget."+closes)),
			wantSteps:    adoptedNote("backstop-synthetic blocked overridden by genuine success self-report; PR opened on relayed branch"),
		}),
		adopts(prCase{
			name:         "an adopted PR intent that already closes the issue is not given a second reference",
			wantStatuses: []Status{detailOnly, StatusAdopted, StatusVerifiedMerged},
			result:       boxRun(true, synthetic, ready, "feat: add widget\n\nCloses #1"),
			check:        openedDraftPR("feat: add widget", "Closes #1"),
		}),
		adopts(prCase{
			name:         "a missing outcome line with a success self-report is adopted",
			wantStatuses: []Status{StatusAdopted, StatusVerifiedMerged},
			result:       boxRun(false, "", ready, adoptIntent),
			check:        allOf(g.relayedBundle(), openedDraftPR("feat: add widget", "Adds a widget."+closes)),
			wantSteps:    adoptedNote("no outcome line; genuine success self-report and relayed bundle; PR opened on relayed branch"),
		}),
		adopts(prCase{
			name:         "an adoption without a PR intent opens the PR from the issue's title",
			wantStatuses: []Status{detailOnly, StatusAdopted, StatusVerifiedMerged},
			result:       boxRun(true, synthetic, ready, ""),
			setup: g.readOnly(0, func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.SetIssue(forge.Issue{Number: "1", Title: "Fix the widget", Labels: []string{"agent-in-progress"}})
			}),
			check: func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
				t.Helper()
				if len(fc.CreateDraftPRCalls) != 1 {
					t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
				}
				call := fc.CreateDraftPRCalls[0]
				if call.Title != "Fix the widget" || call.Base != "main" || call.Head != handoffBranch || !strings.Contains(call.Body, "Closes #1") {
					t.Errorf("CreateDraftPRCalls[0] = %+v, want the issue's title, base main, head %s and a Closes #1 body", call, handoffBranch)
				}
			},
		}),
		// CI, not the driver's self-report, is the authoritative judge of an adopted PR.
		adopts(prCase{
			name: "an adopted PR whose CI is red is not merged and fails the issue", result: boxRun(true, synthetic, ready, adoptIntent),
			wantStatuses: []Status{detailOnly, StatusAdopted, StatusFailed},
			checks:       []forge.RollupState{red},
			want:         Settlement{Verdict: ForReason(dispatchrecord.ReasonCIRed), PRURL: testPR, Note: "ci-red: still red after exhausting 0 fix pass(es)"},
			wantPRClosed: true, wantLabels: failed, wantNoLabels: complete,
			check: draftPRCalls(1),
		}),

		refuses(prCase{
			name:         "a genuine blocked outcome is not adopted whatever the self-report says",
			wantStatuses: []Status{detailOnly, StatusBlocked},
			result:       boxRun(true, genuine, ready, ""),
			want:         boxBlocked, wantComments: noteComment,
		}),
		refuses(prCase{
			name:         "a synthetic blocked outcome without a self-report is not adopted",
			wantStatuses: []Status{detailOnly, StatusBlocked},
			result:       boxRun(true, synthetic, "", ""),
			want:         boxBlocked, wantComments: noteComment,
		}),
		refuses(prCase{
			name:         "a synthetic blocked outcome whose self-report says blocked is not adopted",
			wantStatuses: []Status{detailOnly, StatusBlocked},
			result:       boxRun(true, synthetic, outcome.StatusBlocked, ""),
			want:         boxBlocked, wantComments: noteComment,
		}),
		refuses(prCase{
			name:         "a success fingerprint whose bundle will not relay falls back to blocked",
			wantStatuses: []Status{detailOnly, StatusBlocked},
			result:       boxRun(true, synthetic, ready, ""),
			setup:        g.readOnly(0, func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.RelayBundleErr = errBundleMissing }),
			want:         boxBlocked, wantComments: noteComment,
		}),
		{
			name: "a success fingerprint under a read-write Code Forge is not adopted", noPR: true,
			wantStatuses: []Status{detailOnly, StatusBlocked},
			result:       boxRun(true, synthetic, ready, ""),
			want:         boxBlocked, wantPRClosed: true, wantComments: noteComment,
			check: draftPRCalls(0), wantLabels: failed, wantNoLabels: complete,
		},
		refuses(prCase{
			name:         "no outcome line and no self-report is not adopted",
			wantStatuses: []Status{StatusMissing},
			result:       boxRun(false, "", "", ""),
			want:         noOutcome,
		}),
		refuses(prCase{
			name:         "no outcome line whose self-report says blocked is not adopted",
			wantStatuses: []Status{StatusMissing},
			result:       boxRun(false, "", outcome.StatusBlocked, ""),
			want:         noOutcome,
		}),
		refuses(prCase{
			name:         "no outcome line whose success self-report has no bundle falls back to blocked",
			wantStatuses: []Status{StatusMissing},
			result:       boxRun(false, "", ready, ""),
			setup:        g.readOnly(0, func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.RelayBundleErr = errBundleMissing }),
			want:         noOutcome,
		}),

		// Recover's arm: it needs neither a synthetic outcome nor a read-only Code Forge.
		adopts(prCase{
			name: "recover adopts a relayed branch on a success self-report", entry: entryRelayed,
			wantStatuses: []Status{StatusAdopted, StatusVerifiedMerged},
			checks:       registered,
			check:        draftPRCalls(1),
		}),
		adopts(prCase{
			name: "recover records the adopted PR but clears the recorded pass despite the run's passes", entry: entryRelayed,
			wantStatuses: []Status{StatusAdopted, StatusVerifiedMerged},
			shape:        shapeLocalReadOnly,
			result:       &dispatch.Result{Resolved: selfReportReady().Resolved, Passes: implementThenLandPasses()},
			check: func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
				t.Helper()
				if want := []forge.RecordLandingCall{{Num: "1", Landing: testPR}}; len(fc.RecordLandingCalls) != 1 || fc.RecordLandingCalls[0] != want[0] {
					t.Errorf("RecordLanding calls = %+v, want %+v", fc.RecordLandingCalls, want)
				}
				if want := []forge.RecordLandingPassCall{{Num: "1"}}; len(fc.RecordLandingPassCalls) != 1 || fc.RecordLandingPassCalls[0] != want[0] {
					t.Errorf("RecordLandingPass calls = %+v, want %+v", fc.RecordLandingPassCalls, want)
				}
			},
		}),
		// The parked-relay recovery path (issue #4651): the issue wears only
		// agent-failed, and landing the preserved bundle must not keep it.
		adopts(prCase{
			name: "recover lands a parked failed issue and drops its stale failed label", entry: entryRelayed,
			wantStatuses: []Status{StatusAdopted, StatusVerifiedMerged},
			labels:       failed,
			result:       parked,
			// The parked run's own outcome line, resolved the way recover does.
			setup: g.readOnly(0, func(t *testing.T, _ *forge.Fake, _ *Config) {
				logPath := filepath.Join(t.TempDir(), "issue-1.log")
				line := "SPINDRIFT_OUTCOME issue=1 landing=" + handoffBranch + " status=ready note=ok\n"
				if err := os.WriteFile(logPath, []byte(line), 0o600); err != nil {
					t.Fatal(err)
				}
				resolved, err := outcome.Resolve([]outcome.PassLog{{Path: logPath}}, "")
				if err != nil {
					t.Fatalf("outcome.Resolve: %v", err)
				}
				parked.Resolved = resolved
			}),
			check: g.relayedBundle(),
		}),
		// A decline leaves the labels to recover's own no-open-PR fallback.
		declines(prCase{
			name: "recover does not adopt a self-report that is not a success", result: boxRun(false, "", outcome.StatusBlocked, ""),
			wantErr: []error{ErrNothingToRecover},
		}),
		declines(prCase{
			name: "recover surfaces a bundle that will not relay without touching the issue", result: &dispatch.Result{Resolved: selfReportReady().Resolved},
			wantStatuses: nil,
			setup:        g.readOnly(0, func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.RelayBundleErr = errBundleMissing }),
			wantErr:      []error{errRelayBundle, errBundleMissing},
		}),
		declines(prCase{
			name: "recover names the draft PR stage when opening the PR fails", result: &dispatch.Result{Resolved: selfReportReady().Resolved},
			wantStatuses: []Status{StatusDraftPRFinalLookup},
			setup:        g.readOnly(0, func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.CreateDraftPRErr = errDraft500 }),
			wantErr:      []error{errCreateDraftPR, errDraft500}, wantNoErr: []error{errRelayBundle},
		}),
	}
}
