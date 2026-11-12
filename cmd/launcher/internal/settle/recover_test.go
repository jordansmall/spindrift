package settle

import (
	"errors"
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/passmanifest"
)

const (
	recoverNum = "2225"
	recoverPR  = "https://github.com/owner/repo/pull/2225"
)

var errRecoverBegin = errors.New("claim lost")

// recoverEnv is a PR-forge Settle with a Begin that counts its calls.
type recoverEnv struct {
	fc     *forge.Fake
	s      *Settle
	c      Config
	outbox string
	begins int
	beginE error
	steps  *ProgressRecorder
}

func newRecoverEnv(t *testing.T, bundle bool) *recoverEnv {
	t.Helper()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: recoverNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = recoverPR
	fc.SetCheckStates(recoverPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	outbox := t.TempDir()
	if bundle {
		writeBundle(t, outbox)
	}
	c := baseConfig()
	c.OutboxDir = func(string) string { return outbox }
	c.BaseBranch = "main"
	steps := &ProgressRecorder{}
	c.Progress = steps
	return &recoverEnv{fc: fc, c: c, outbox: outbox, steps: steps, s: newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())}
}

func (e *recoverEnv) in(openPR string, resolved outcome.Resolved, passes []passmanifest.Entry, strict bool) RecoverInput {
	return RecoverInput{
		Num: recoverNum, OpenPR: openPR, Resolved: resolved, Passes: passes, Strict: strict,
		Begin: func() (dispatch.Dispatcher, error) {
			e.begins++
			return dispatch.NewFake(), e.beginE
		},
	}
}

func readyReport() outcome.Resolved {
	return outcome.Resolved{SelfReportFound: true, SelfReport: outcome.SelfReport{Status: outcome.StatusReady}}
}

func (e *recoverEnv) assertSettledNothing(t *testing.T) {
	t.Helper()
	assertNoTransitionOrComment(t, e.fc)
	if len(e.fc.RelayBundleCalls) != 0 || len(e.fc.CreateDraftPRCalls) != 0 || e.fc.Merged != "" {
		t.Errorf("nothing may land; relay=%+v draft=%+v merged=%q", e.fc.RelayBundleCalls, e.fc.CreateDraftPRCalls, e.fc.Merged)
	}
}

// An open PR does not vouch for a run whose success self-report is stale
// (issue #5973), so Recover declines it before it claims the issue.
func TestRecover_UnreviewedOpenPRWithSuccessReportDeclinesBeforeBegin(t *testing.T) {
	e := newRecoverEnv(t, true)
	st, err := e.s.Recover(e.in(recoverPR, readyReport(), implementReadyPasses(), false))
	want := Settlement{Verdict: ForReason(dispatchrecord.ReasonUnreviewed), Note: dispatchrecord.UnreviewedNote}
	if err != nil || st != want {
		t.Fatalf("Recover = %+v, %v; want %+v, nil", st, err, want)
	}
	if e.begins != 0 {
		t.Errorf("Begin called %d times, want 0", e.begins)
	}
	e.assertSettledNothing(t)
}

// Without a success self-report the loop stopped in implement or fix (issue
// #5984): an open PR is left to the adopt arm's merge gate.
func TestRecover_UnreviewedOpenPRWithoutSuccessReportAdopts(t *testing.T) {
	e := newRecoverEnv(t, false)
	// The adopt arm does not trust an immediate SUCCESS (issue #1652): a leading
	// PENDING proves the run's own checks registered.
	e.fc.SetCheckStates(recoverPR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
	st, err := e.s.Recover(e.in(recoverPR, outcome.Resolved{}, fixLastPasses(), false))
	if err != nil {
		t.Fatalf("Recover err = %v, want nil", err)
	}
	if e.begins != 1 {
		t.Errorf("Begin called %d times, want 1", e.begins)
	}
	if st.State() != forge.Complete || st.PRURL != recoverPR || e.fc.Merged != recoverPR {
		t.Errorf("Settlement = %+v, merged %q; want the open PR adopted and merged", st, e.fc.Merged)
	}
	if len(e.fc.CreateDraftPRCalls) != 0 {
		t.Errorf("an open PR must not get a second draft: %+v", e.fc.CreateDraftPRCalls)
	}
}

// With no PR to adopt, an unreviewed run is declined whether or not the
// self-report is a success: push-only treats a bundle alone as evidence, which
// an unreviewed run must not be (issues #5973, #5984).
func TestRecover_UnreviewedWithoutPRDeclinesBeforeBegin(t *testing.T) {
	cases := []struct {
		name     string
		resolved outcome.Resolved
		passes   []passmanifest.Entry
	}{
		{"ready-from-implement", readyReport(), implementReadyPasses()},
		{"no-report-fix-last", outcome.Resolved{}, fixLastPasses()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRecoverEnv(t, true)
			st, err := e.s.Recover(e.in("", tc.resolved, tc.passes, false))
			want := Settlement{Verdict: ForReason(dispatchrecord.ReasonUnreviewed), Note: dispatchrecord.UnreviewedNote}
			if err != nil || st != want {
				t.Fatalf("Recover = %+v, %v; want %+v, nil", st, err, want)
			}
			if e.begins != 0 {
				t.Errorf("Begin called %d times, want 0", e.begins)
			}
			e.assertSettledNothing(t)
		})
	}
}

// Queue mode (Strict) only takes a branch with a bundle in the outbox and a
// success self-report, and skips an unreviewed one silently: settle already
// commented on the issue, so a decline here would repeat every daemon sweep.
func TestRecover_StrictDeclinesIneligibleBeforeBegin(t *testing.T) {
	cases := []struct {
		name     string
		bundle   bool
		resolved outcome.Resolved
		passes   []passmanifest.Entry
	}{
		{"no-bundle", false, readyReport(), nil},
		{"no-success-report", true, outcome.Resolved{}, nil},
		// Issue #2981: only status=ready is a success; the bare word never was.
		{"bare-success-word", true, outcome.Resolved{SelfReportFound: true, SelfReport: outcome.SelfReport{Status: "success"}}, nil},
		{"blocked-report", true, outcome.Resolved{SelfReportFound: true, SelfReport: outcome.SelfReport{Status: outcome.StatusBlocked}}, nil},
		{"unreviewed", true, readyReport(), implementReadyPasses()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRecoverEnv(t, tc.bundle)
			st, err := e.s.Recover(e.in("", tc.resolved, tc.passes, true))
			if !errors.Is(err, ErrNothingToRecover) || st != (Settlement{}) {
				t.Fatalf("Recover = %+v, %v; want zero, ErrNothingToRecover", st, err)
			}
			if e.begins != 0 {
				t.Errorf("Begin called %d times, want 0", e.begins)
			}
			e.assertSettledNothing(t)
		})
	}
}

func TestRecover_StrictWithBundleAndSuccessReportRelays(t *testing.T) {
	e := newRecoverEnv(t, true)
	st, err := e.s.Recover(e.in("", readyReport(), nil, true))
	if err != nil || st.State() != forge.Complete || st.PRURL != recoverPR {
		t.Fatalf("Recover = %+v, %v; want complete naming %s", st, err, recoverPR)
	}
	if e.begins != 1 || e.fc.Merged != recoverPR {
		t.Errorf("begins = %d, merged = %q; want 1, %s", e.begins, e.fc.Merged, recoverPR)
	}
}

func TestRecover_BeginErrorReturnedUnchangedAndNothingSettled(t *testing.T) {
	for _, openPR := range []string{"", recoverPR} {
		t.Run("openPR="+openPR, func(t *testing.T) {
			e := newRecoverEnv(t, true)
			e.beginE = errRecoverBegin
			st, err := e.s.Recover(e.in(openPR, readyReport(), nil, false))
			if err != errRecoverBegin || st != (Settlement{}) {
				t.Fatalf("Recover = %+v, %v; want zero, %v", st, err, errRecoverBegin)
			}
			e.assertSettledNothing(t)
		})
	}
}

// The relay lands at generation 0 and returns the landing's Settlement.
func TestRecover_PRForgeRelayReturnsLandingSettlement(t *testing.T) {
	e := newRecoverEnv(t, false)
	st, err := e.s.Recover(e.in("", readyReport(), nil, false))
	if err != nil {
		t.Fatalf("Recover err = %v, want nil", err)
	}
	if st.State() != forge.Complete || st.PRURL != recoverPR {
		t.Errorf("Settlement = %+v, want complete naming %s", st, recoverPR)
	}
	if len(e.fc.CreateDraftPRCalls) != 1 || e.fc.Merged != recoverPR {
		t.Errorf("draft PRs = %+v, merged = %q; want one draft merged", e.fc.CreateDraftPRCalls, e.fc.Merged)
	}
	var got []Status
	for _, st := range e.steps.Steps() {
		got = append(got, st.Status)
	}
	if want := []Status{StatusAdopted, StatusVerifiedMerged}; !slices.Equal(got, want) {
		t.Errorf("recorded steps = %v, want %v", got, want)
	}
	iss, _ := e.fc.Issue(recoverNum)
	if !containsLabel(iss.Labels, "agent-complete") || containsLabel(iss.Labels, "agent-in-progress") {
		t.Errorf("labels = %v, want agent-complete without agent-in-progress", iss.Labels)
	}
}

// A run with no success self-report has nothing to relay on a PR forge: the
// claim is already taken by then, so Begin ran.
func TestRecover_NothingRelayedAfterBeginIsErrNothingToRecover(t *testing.T) {
	e := newRecoverEnv(t, false)
	st, err := e.s.Recover(e.in("", outcome.Resolved{}, nil, false))
	if !errors.Is(err, ErrNothingToRecover) || st != (Settlement{}) {
		t.Fatalf("Recover = %+v, %v; want zero, ErrNothingToRecover", st, err)
	}
	if e.begins != 1 {
		t.Errorf("Begin called %d times, want 1", e.begins)
	}
	e.assertSettledNothing(t)
}

// Both relay stages' errors survive for main's workflow-permission note and
// recoverFailed to read, with the issue's labels left alone.
func TestRecover_RelayFailureErrorsSurvive(t *testing.T) {
	t.Run("relay", func(t *testing.T) {
		e := newRecoverEnv(t, false)
		e.fc.RelayBundleErr = errors.New("bundle missing")
		_, err := e.s.Recover(e.in("", readyReport(), nil, false))
		if !errors.Is(err, errRelayBundle) || !errors.Is(err, e.fc.RelayBundleErr) {
			t.Fatalf("Recover err = %v, want errRelayBundle wrapping %v", err, e.fc.RelayBundleErr)
		}
		assertRecoverFailureLeftIssueAlone(t, e)
	})
	t.Run("draft PR", func(t *testing.T) {
		e := newRecoverEnv(t, false)
		e.fc.CreateDraftPRErr = errors.New("create draft PR: 500")
		_, err := e.s.Recover(e.in("", readyReport(), nil, false))
		if !errors.Is(err, errCreateDraftPR) || !errors.Is(err, e.fc.CreateDraftPRErr) {
			t.Fatalf("Recover err = %v, want errCreateDraftPR wrapping %v", err, e.fc.CreateDraftPRErr)
		}
		if errors.Is(err, errRelayBundle) {
			t.Errorf("err = %v must not name the relay stage", err)
		}
		assertRecoverFailureLeftIssueAlone(t, e)
	})
}

func assertRecoverFailureLeftIssueAlone(t *testing.T, e *recoverEnv) {
	t.Helper()
	assertNoTransitionOrComment(t, e.fc)
	if e.fc.Merged != "" {
		t.Errorf("nothing may merge; fc.Merged=%q", e.fc.Merged)
	}
	iss, _ := e.fc.Issue(recoverNum)
	if !containsLabel(iss.Labels, "agent-in-progress") || containsLabel(iss.Labels, "agent-failed") || containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue labels must stay untouched; labels=%v", iss.Labels)
	}
}
