package settle

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/passmanifest"
	"spindrift.dev/launcher/internal/retry"
)

const pushOnlyBranch = "agent/issue-1"

// pushOnlyEnv is what a row's setup may adjust before Settle runs. s is filled
// in once the Settle exists, for hooks that need to mark a stop on it.
type pushOnlyEnv struct {
	c       Config
	fc      *forge.Fake
	outbox  string
	sleeps  *[]time.Duration
	s       *Settle
	passes  []passmanifest.Entry
	landing string // the landing= the Box printed
}

type pushOnlyEntry int

const (
	entrySettle pushOnlyEntry = iota
	entryRelayed
	entryAdopted
)

type bundleFate int

const (
	bundleUnset bundleFate = iota // a bundle:true row must set wantBundle explicitly
	bundleKept
	bundleDropped
)

// pushOnlyCase is one push-only landing observed through Settle's public
// entry point: the Settlement, the progress steps, the tracker's labels and
// the side effects the Lander owns on the forge fake.
type pushOnlyCase struct {
	name      string
	entry     pushOnlyEntry                      // which public entry reaches the landing
	labels    []string                           // the issue's labels going in; default agent-in-progress
	shaped    bool                               // wrap the tracker as LocalShaped even with no passes
	result    func(*pushOnlyEnv) dispatch.Result // default: a ready outcome, or a ready self-report for Recover
	local     bool                               // BundleRelay + LandingRef shape (CODE_FORGE=local); else git
	mergeMode string                             // default immediate
	bundle    bool                               // seed seam.bundle in the outbox
	setup     func(*pushOnlyEnv)                 // before the Settle exists
	arm       func(*pushOnlyEnv)                 // once e.s exists, ahead of Settle

	want         Settlement
	abandoned    bool // the stop left the released issue untouched
	wantSteps    []Step
	wantLabels   []string
	wantNoLabels []string
	wantMerged   string
	wantRelays   int
	wantRebases  int
	wantComments []string // substring of each comment, in order
	wantBundle   bundleFate
	wantLandings []forge.RecordLandingCall
	wantPasses   []forge.RecordLandingPassCall
	check        func(*testing.T, *pushOnlyEnv)
}

func selfReportReady() dispatch.Result {
	return dispatch.Result{Resolved: outcome.Resolved{
		SelfReportFound: true,
		SelfReport:      outcome.SelfReport{Status: outcome.StatusReady},
	}}
}

func pushOnlyStep(status Status, detail string) Step {
	return Step{Key: "1", Landing: pushOnlyBranch, Status: status, Detail: detail}
}

func containsSteps(got, want []Step) (missing []Step) {
	rest := append([]Step(nil), got...)
	for _, w := range want {
		found := false
		for i, g := range rest {
			if g == w {
				rest = append(rest[:i], rest[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, w)
		}
	}
	return missing
}

func TestSettle_PushOnlyLander(t *testing.T) {
	merged := Settlement{Verdict: ForReason(dispatchrecord.ReasonMerged)}
	blocked := Settlement{Verdict: ForReason(dispatchrecord.ReasonMergeBlocked)}
	manual := Settlement{Verdict: ForReason(dispatchrecord.ReasonManual)}
	complete := []string{"agent-complete"}
	noFailed := []string{"agent-failed"}
	errPush := errors.New("remote rejected: non-fast-forward")
	errRebase := errors.New("rebase: worktree dirty")
	errBlip := errors.New("ssh auth blip")
	errTransient := forge.ErrTransientPushFailure.Error()
	errTransientMerge := forge.ErrMergeTransient.Error()
	placeholder := []forge.RecordLandingCall{{Num: "1", Landing: pushOnlyBranch}}
	refUpgrade := []forge.RecordLandingCall{{Num: "1", Landing: pushOnlyBranch}, {Num: "1", Landing: "integration/1694@abc123"}}
	// Recover records the landing once itself and again in the push-only Lander.
	twice := func(l []forge.RecordLandingCall) []forge.RecordLandingCall {
		return append(append([]forge.RecordLandingCall(nil), l...), l...)
	}
	// SettleAdopted names the branch as the Settlement's PRURL.
	adoptedManual := Settlement{Verdict: manual.Verdict, PRURL: pushOnlyBranch}
	adoptedMerged := Settlement{Verdict: merged.Verdict, PRURL: pushOnlyBranch}
	markNow := func(e *pushOnlyEnv) { e.s.Registry().Mark("1") }
	markDuring := func(method string) func(*pushOnlyEnv) {
		return func(e *pushOnlyEnv) { e.fc.On(method, func(int) { e.s.Registry().Mark("1") }) }
	}
	relayedOnce := func(t *testing.T, e *pushOnlyEnv) {
		t.Helper()
		want := []forge.RelayBundleCall{{OutboxDir: e.outbox, Ref: pushOnlyBranch}}
		if !reflect.DeepEqual(e.fc.RelayBundleCalls, want) {
			t.Errorf("RelayBundle calls = %+v, want %+v", e.fc.RelayBundleCalls, want)
		}
	}
	unclaimed := func(e *pushOnlyEnv) {
		e.c.Unclaimed = true
		e.fc.SetIssue(forge.Issue{Number: "1", Labels: append([]string{}, butlerPatchLabels...)})
	}
	noTransition := func(t *testing.T, e *pushOnlyEnv) {
		t.Helper()
		if len(e.fc.TransitionStateCalls) != 0 {
			t.Errorf("an unclaimed landing that did not merge must not transition; got %+v", e.fc.TransitionStateCalls)
		}
	}
	var perIssue *forge.Fake

	cases := []pushOnlyCase{
		// git shape: merge modes
		{
			name:      "git manual leaves the branch as pushed",
			mergeMode: "manual",
			want:      manual, wantLabels: complete,
			wantSteps: []Step{pushOnlyStep(StatusAgentComplete, "merge-mode=manual")},
		},
		{
			name: "git immediate pushes to the target branch",
			want: merged, wantLabels: complete, wantMerged: pushOnlyBranch,
		},
		{
			name:  "git push failure stays complete with a merge-blocked comment",
			setup: func(e *pushOnlyEnv) { e.fc.MergeErr = errPush },
			want:  blocked, wantLabels: complete, wantNoLabels: noFailed,
			wantSteps:    []Step{pushOnlyStep(StatusMergeBlocked, "!! remote rejected: non-fast-forward")},
			wantComments: []string{"landing blocked: remote rejected: non-fast-forward"},
		},
		{
			name:      "git auto merge needs a PR forge so it blocks",
			mergeMode: "auto",
			want:      blocked, wantLabels: complete,
			wantSteps:    []Step{pushOnlyStep(StatusMergeBlocked, "!! MERGE_MODE=auto requires a Code Forge with PR support (got a push-only forge)")},
			wantComments: []string{"landing blocked: MERGE_MODE=auto requires a Code Forge with PR support (got a push-only forge)"},
		},
		{
			name:      "git unrecognised merge mode blocks",
			mergeMode: "bogus",
			want:      blocked, wantLabels: complete,
			wantSteps:    []Step{pushOnlyStep(StatusMergeBlocked, `!! unrecognised MERGE_MODE: "bogus"`)},
			wantComments: []string{`landing blocked: unrecognised MERGE_MODE: "bogus"`},
		},
		// git shape: a stop abandons without touching the released issue
		{
			name:      "git stop before landing skips complete and merge",
			arm:       markNow,
			abandoned: true,
		},
		{
			name: "git stop during merge backoff abandons",
			setup: func(e *pushOnlyEnv) {
				e.c.Policy = retry.Policy{Max: 1, Unit: time.Second}
				e.fc.MergeErrs = []error{forge.ErrMergeTransient, nil}
				e.c.Clock = dispatch.Clock{Now: time.Now, Sleep: func(time.Duration) { e.s.Registry().Mark("1") }}
			},
			abandoned: true,
			wantSteps: []Step{pushOnlyStep(StatusMergeTransientRetry, "attempt=1/1  !! "+errTransientMerge)},
		},
		{
			name:      "git stop inside a merge that succeeds drops the landed bundle",
			bundle:    true,
			arm:       markDuring("Merge"),
			abandoned: true, wantMerged: pushOnlyBranch, wantBundle: bundleDropped,
		},
		{
			name:      "git stop inside a merge that fails keeps the bundle",
			bundle:    true,
			setup:     func(e *pushOnlyEnv) { e.fc.MergeErr = errors.New("required review missing") },
			arm:       markDuring("Merge"),
			abandoned: true, wantBundle: bundleKept,
		},
		// git shape: rebase and merge retries
		{
			name:  "git conflict rebases then retries the merge without draft flips",
			setup: func(e *pushOnlyEnv) { e.c.MaxRebaseAttempts = 3; e.fc.MergeErrs = []error{forge.ErrMergeConflict} },
			want:  merged, wantLabels: complete, wantMerged: pushOnlyBranch, wantRebases: 1,
			wantSteps: []Step{pushOnlyStep(StatusRebaseRetry, "attempt=1/3")},
			check: func(t *testing.T, e *pushOnlyEnv) {
				if len(e.fc.MarkDraftCalls) != 0 || len(e.fc.MarkReadyCalls) != 0 {
					t.Errorf("push-only forge must never flip draft state; MarkDraft=%v MarkReady=%v", e.fc.MarkDraftCalls, e.fc.MarkReadyCalls)
				}
			},
		},
		{
			name: "git rebase failing for a non-conflict reason blocks the merge",
			setup: func(e *pushOnlyEnv) {
				e.c.MaxRebaseAttempts = 3
				e.fc.MergeErrs = []error{forge.ErrMergeConflict}
				e.fc.RebaseErr = errRebase
			},
			want: blocked, wantLabels: complete, wantNoLabels: noFailed, wantRebases: 1,
			wantSteps: []Step{
				pushOnlyStep(StatusRebaseRetry, "attempt=1/3"),
				pushOnlyStep(StatusRebaseFailed, "!! rebase: worktree dirty"),
				pushOnlyStep(StatusMergeBlocked, "!! rebase: worktree dirty"),
			},
			wantComments: []string{"landing blocked: rebase: worktree dirty"},
		},
		{
			// Push-only has no Dispatcher, so a conflicting rebase cannot be resolved.
			name: "git rebase that itself conflicts has no resolver so blocks",
			setup: func(e *pushOnlyEnv) {
				e.c.MaxRebaseAttempts = 3
				e.fc.MergeErrs = []error{forge.ErrMergeConflict}
				e.fc.RebaseErr = forge.ErrMergeConflict
			},
			want: blocked, wantLabels: complete, wantNoLabels: noFailed, wantRebases: 1,
			wantSteps: []Step{
				pushOnlyStep(StatusRebaseFailed, "!! "+forge.ErrMergeConflict.Error()),
				pushOnlyStep(StatusMergeBlocked, "!! "+forge.ErrMergeConflict.Error()),
			},
			wantComments: []string{"landing blocked: " + forge.ErrMergeConflict.Error()},
		},
		{
			name: "git transient rebase push failure retries then merges",
			setup: func(e *pushOnlyEnv) {
				e.c.MaxRebaseAttempts = 3
				e.fc.MergeErrs = []error{forge.ErrMergeConflict}
				e.fc.RebaseErrs = []error{forge.ErrTransientPushFailure, nil}
			},
			want: merged, wantLabels: complete, wantMerged: pushOnlyBranch, wantRebases: 2,
			wantSteps: []Step{pushOnlyStep(StatusRebasePushRetry, "attempt=1/3  !! "+errTransient)},
		},
		{
			name: "git rebase push retries exhausted blocks the merge",
			setup: func(e *pushOnlyEnv) {
				e.c.MaxRebaseAttempts = 2
				e.fc.MergeErrs = []error{forge.ErrMergeConflict}
				e.fc.RebaseErr = forge.ErrTransientPushFailure
			},
			want: blocked, wantLabels: complete, wantNoLabels: noFailed, wantRebases: 3,
			wantSteps: []Step{
				pushOnlyStep(StatusRebasePushRetry, "attempt=1/2  !! "+errTransient),
				pushOnlyStep(StatusRebasePushRetry, "attempt=2/2  !! "+errTransient),
				pushOnlyStep(StatusRebasePushRetriesExhausted, "attempts=2  !! "+errTransient),
			},
			wantComments: []string{"landing blocked: " + errTransient},
		},
		{
			name: "git merge blocked by checks waits then merges",
			setup: func(e *pushOnlyEnv) {
				e.c.MaxRebaseAttempts = 3
				e.fc.MergeErrs = []error{forge.ErrMergeBlockedByChecks, nil}
			},
			want: merged, wantLabels: complete, wantMerged: pushOnlyBranch,
			wantSteps: []Step{pushOnlyStep(StatusMergeBlockedByChecks, "attempt=1/3")},
			check: func(t *testing.T, e *pushOnlyEnv) {
				if want := []time.Duration{time.Second}; !reflect.DeepEqual(*e.sleeps, want) {
					t.Errorf("clock sleeps = %v, want %v", *e.sleeps, want)
				}
			},
		},
		{
			name:  "git merge blocked by checks with no attempt budget blocks",
			setup: func(e *pushOnlyEnv) { e.fc.MergeErr = forge.ErrMergeBlockedByChecks },
			want:  blocked, wantLabels: complete, wantNoLabels: noFailed,
			wantComments: []string{"landing blocked: " + forge.ErrMergeBlockedByChecks.Error()},
		},
		{
			name: "git transient merge failure retries then merges",
			setup: func(e *pushOnlyEnv) {
				e.c.Policy = retry.Policy{Max: 2, Unit: time.Second}
				e.fc.MergeErrs = []error{forge.ErrMergeTransient, nil}
			},
			want: merged, wantLabels: complete, wantMerged: pushOnlyBranch,
			wantSteps: []Step{pushOnlyStep(StatusMergeTransientRetry, "attempt=1/2  !! "+errTransientMerge)},
		},
		// git shape: an unclaimed issue only transitions on an actual merge
		{
			name:  "git unclaimed immediate merge still completes the issue",
			setup: unclaimed,
			want:  merged, wantLabels: complete, wantNoLabels: noFailed, wantMerged: pushOnlyBranch,
		},
		{
			// An unclaimed landing that does not merge latches no verdict, so Settle returns the zero Settlement.
			name:         "git unclaimed manual leaves the issue untouched",
			mergeMode:    "manual",
			setup:        unclaimed,
			wantNoLabels: []string{"agent-complete", "agent-failed"},
			check:        noTransition,
		},
		{
			name:         "git unclaimed merge failure comments without transitioning",
			setup:        func(e *pushOnlyEnv) { unclaimed(e); e.fc.MergeErr = errPush },
			wantNoLabels: []string{"agent-complete", "agent-failed"},
			wantComments: []string{"landing blocked: remote rejected: non-fast-forward"},
			check:        noTransition,
		},
		// local shape: the bundle is relayed in ahead of the merge
		{
			name: "local manual relays the bundle and keeps it", local: true, mergeMode: "manual", bundle: true,
			want: manual, wantLabels: complete, wantRelays: 1, wantBundle: bundleKept, wantLandings: placeholder,
		},
		{
			name: "local immediate relays then merges and drops the bundle", local: true, bundle: true,
			want: merged, wantLabels: complete, wantMerged: pushOnlyBranch, wantRelays: 1, wantBundle: bundleDropped,
			check: relayedOnce,
		},
		{
			name: "local immediate records the resolved LandingRef after the merge", local: true,
			setup: func(e *pushOnlyEnv) { e.fc.LandingRefValue = "integration/1694@abc123" },
			want:  merged, wantMerged: pushOnlyBranch, wantRelays: 1, wantLandings: refUpgrade,
			check: relayedOnce,
		},
		{
			name: "local LandingRef upgrade carries the pass", local: true,
			setup: func(e *pushOnlyEnv) {
				e.fc.LandingRefValue = "integration/1694@abc123"
				e.passes = implementThenLandPasses()
			},
			want: merged, wantMerged: pushOnlyBranch, wantRelays: 1, wantLandings: refUpgrade,
			wantPasses: []forge.RecordLandingPassCall{{Num: "1", Pass: 2, Kind: "land"}, {Num: "1", Pass: 2, Kind: "land"}},
		},
		{
			name: "local manual records the pass with the placeholder", local: true, mergeMode: "manual",
			setup: func(e *pushOnlyEnv) { e.passes = implementThenLandPasses() },
			want:  manual, wantRelays: 1, wantLandings: placeholder,
			wantPasses: []forge.RecordLandingPassCall{{Num: "1", Pass: 2, Kind: "land"}},
		},
		{
			name: "local LandingRef failure stays merged without recording", local: true,
			setup: func(e *pushOnlyEnv) { e.fc.LandingRefErr = errors.New("integration branch vanished") },
			want:  merged, wantMerged: pushOnlyBranch, wantRelays: 1, wantLandings: placeholder,
			wantSteps: []Step{pushOnlyStep(StatusLandingRefUnresolved, "!! integration branch vanished")},
		},
		{
			name: "local conflict rebases then retries the merge", local: true,
			setup: func(e *pushOnlyEnv) { e.c.MaxRebaseAttempts = 3; e.fc.MergeErrs = []error{forge.ErrMergeConflict} },
			want:  merged, wantLabels: complete, wantMerged: pushOnlyBranch, wantRelays: 1, wantRebases: 1,
			wantSteps: []Step{pushOnlyStep(StatusRebaseRetry, "attempt=1/3")},
		},
		{
			name: "local merge conflict after the relay blocks instead of failing", local: true,
			setup: func(e *pushOnlyEnv) { e.fc.MergeErr = forge.ErrMergeConflict },
			want:  blocked, wantLabels: complete, wantNoLabels: noFailed, wantRelays: 1,
			wantComments: []string{"landing blocked: " + forge.ErrMergeConflict.Error()},
		},
		{
			name: "local merge and relay use the per-issue Code Forge", local: true,
			setup: func(e *pushOnlyEnv) {
				perIssue = forge.NewFake()
				perIssue.BranchPrefix = "agent/issue-"
				e.c.CodeForgeForIssue = func(string) forge.CodeForge { return perIssue.AsLocal() }
			},
			want: merged, wantLabels: complete,
			check: func(t *testing.T, e *pushOnlyEnv) {
				if perIssue.Merged != pushOnlyBranch || len(perIssue.RelayBundleCalls) != 1 {
					t.Errorf("per-issue forge: Merged=%q relays=%+v, want Merge(%q) after one relay", perIssue.Merged, perIssue.RelayBundleCalls, pushOnlyBranch)
				}
			},
		},
		{
			name: "local unset OutboxDir blocks loudly without parking failed", local: true,
			setup: func(e *pushOnlyEnv) { e.c.OutboxDir = nil },
			want:  blocked, wantLabels: complete, wantNoLabels: noFailed,
			wantComments: []string{"Config.OutboxDir is unset"},
		},
		{
			name: "local missing bundle blocks without merging", local: true,
			setup: func(e *pushOnlyEnv) { e.fc.RelayBundleErr = fmt.Errorf("outbox: %w", forge.ErrBundleNotFound) },
			want:  blocked, wantLabels: complete, wantNoLabels: noFailed, wantRelays: 1,
			wantComments: []string{"landing blocked: "},
		},
		{
			name: "local merge-blocked records the agent branch not the outcome landing", local: true,
			setup: func(e *pushOnlyEnv) {
				e.landing = "integration/foo@abc"
				e.fc.RelayBundleErr = fmt.Errorf("outbox: %w", forge.ErrBundleNotFound)
			},
			want: blocked, wantRelays: 1, wantLandings: placeholder,
			wantComments: []string{"landing blocked: "},
		},
		{
			name: "local hostile landing is ignored in favour of the agent branch", local: true,
			setup: func(e *pushOnlyEnv) { e.landing = "main" },
			want:  merged, wantMerged: pushOnlyBranch, wantRelays: 1,
			check: relayedOnce,
		},
		// local shape: relay retries and stops
		{
			name: "local transient relay failure retries then merges", local: true,
			setup: func(e *pushOnlyEnv) {
				e.c.Policy = retry.Policy{Max: 3, Unit: time.Second}
				e.fc.RelayBundleErrs = failThenSucceed(2)
			},
			want: merged, wantMerged: pushOnlyBranch, wantRelays: 3,
			wantSteps: []Step{
				pushOnlyStep(StatusRelayRetry, "attempt=1/3  !! "+errBlip.Error()),
				pushOnlyStep(StatusRelayRetry, "attempt=2/3  !! "+errBlip.Error()),
			},
			check: func(t *testing.T, e *pushOnlyEnv) {
				if len(*e.sleeps) < 2 {
					t.Errorf("backoff sleeps = %v, want at least 2", *e.sleeps)
				}
			},
		},
		{
			name: "local stop during relay abandons before the merge", local: true,
			arm:       markDuring("RelayBundle"),
			abandoned: true, wantRelays: 1,
		},
		{
			name: "local stop during relay backoff abandons without complete", local: true,
			setup: func(e *pushOnlyEnv) {
				e.c.Policy = retry.Policy{Max: 3, Unit: time.Second}
				e.fc.RelayBundleErr = errBlip
				_, e.c.Clock = stopOnRelayBackoff(&e.s, e.fc, "1")
			},
			abandoned: true, wantRelays: 1,
		},
		{
			name: "local relay exhausted parks the issue failed", local: true,
			setup: func(e *pushOnlyEnv) {
				e.c.Policy = retry.Policy{Max: 1, Unit: time.Second}
				e.fc.RelayBundleErr = errBlip
			},
			want:       Settlement{Verdict: ForReason(dispatchrecord.ReasonRelayFailed), Note: "bundle relay failed; work preserved in outbox"},
			wantRelays: 2, wantLandings: placeholder,
			wantSteps:    []Step{pushOnlyStep(StatusRelayFailed, "!! relay bundle failed: "+errBlip.Error()), pushOnlyStep(StatusFailed, "!! ")},
			wantComments: []string{"bundle relay failed: relay bundle failed: ssh auth blip. The Box's bundle is preserved in the outbox; run `spindrift recover 1`"},
			check: func(t *testing.T, e *pushOnlyEnv) {
				if len(e.fc.TransitionStateCalls) != 1 {
					t.Errorf("want exactly one transition, got %+v", e.fc.TransitionStateCalls)
				}
				assertOutboxParked(t, e.fc, "1")
			},
		},
		// recover and adopt reach the same landing through their own entries
		{
			name: "relayed local self-report success lands the relayed branch", entry: entryRelayed, local: true,
			labels: []string{"agent-recoverable"},
			want:   merged, wantLabels: complete, wantNoLabels: noFailed, wantMerged: pushOnlyBranch, wantRelays: 1,
			wantSteps: []Step{pushOnlyStep(StatusAdopted, "note=genuine success self-report; relayed branch landed")},
		},
		{
			name: "relayed local bundle alone lands the relayed branch and drops the bundle", entry: entryRelayed, local: true, bundle: true,
			labels: []string{"agent-recoverable"},
			result: func(*pushOnlyEnv) dispatch.Result { return dispatch.Result{} },
			want:   merged, wantLabels: complete, wantNoLabels: noFailed, wantMerged: pushOnlyBranch, wantRelays: 1, wantBundle: bundleDropped,
			wantSteps: []Step{pushOnlyStep(StatusAdopted, "note=bundle present in outbox; relayed branch landed")},
		},
		{
			name: "relayed local land-last run without a report still lands", entry: entryRelayed, local: true, bundle: true,
			labels: []string{"agent-failed"},
			setup:  func(e *pushOnlyEnv) { e.passes = landLastNoOutcomePasses() },
			result: func(*pushOnlyEnv) dispatch.Result { return dispatch.Result{} },
			want:   merged, wantMerged: pushOnlyBranch, wantRelays: 1, wantBundle: bundleDropped,
			wantLandings: twice(placeholder),
			wantSteps:    []Step{pushOnlyStep(StatusAdopted, "note=bundle present in outbox; relayed branch landed")},
		},
		{
			name: "relayed local manual clears the recorded pass despite the run's passes", entry: entryRelayed, local: true, mergeMode: "manual",
			setup: func(e *pushOnlyEnv) { e.passes = implementThenLandPasses() },
			want:  manual, wantRelays: 1, wantLandings: twice(placeholder),
			wantPasses: []forge.RecordLandingPassCall{{Num: "1"}, {Num: "1"}},
		},
		{
			name: "relayed local manual without passes clears the recorded pass", entry: entryRelayed, local: true, mergeMode: "manual",
			shaped: true,
			want:   manual, wantRelays: 1, wantLandings: twice(placeholder),
			wantPasses: []forge.RecordLandingPassCall{{Num: "1"}, {Num: "1"}},
		},
		{
			name: "adopted local manual records the landing and clears the recorded pass", entry: entryAdopted, local: true, mergeMode: "manual",
			shaped: true,
			want:   adoptedManual, wantRelays: 1, wantLandings: placeholder,
			wantPasses: []forge.RecordLandingPassCall{{Num: "1"}},
		},
		{
			// verifyMerged needs a PR the push-only forge lacks, so the adopted merge must skip it.
			name: "adopted git push-only merge skips PR verification", entry: entryAdopted,
			want: adoptedMerged, wantLabels: complete, wantNoLabels: noFailed, wantMerged: pushOnlyBranch,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig()
			c.MergeMode = "immediate"
			if tc.mergeMode != "" {
				c.MergeMode = tc.mergeMode
			}
			fc := forge.NewFake(testDispatchLabels)
			fc.BranchPrefix = "agent/issue-"
			labels := []string{"agent-in-progress"}
			if tc.labels != nil {
				labels = tc.labels
			}
			fc.SetIssue(forge.Issue{Number: "1", Labels: labels})
			sleeps, clock := recordingClock()
			c.Clock = clock
			env := &pushOnlyEnv{c: c, fc: fc, outbox: t.TempDir(), sleeps: sleeps, landing: pushOnlyBranch}
			if tc.bundle {
				writeBundle(t, env.outbox)
			}
			if tc.local || tc.bundle {
				env.c.OutboxDir = func(string) string { return env.outbox }
			}
			if tc.setup != nil {
				tc.setup(env)
			}
			var tracker forge.IssueTracker = fc
			if env.passes != nil || tc.shaped {
				tracker = fc.AsLocalShaped()
			}
			cf := fc.AsPushOnly()
			if tc.local {
				cf = fc.AsLocal()
			}
			rec := &ProgressRecorder{}
			env.c.Progress = rec
			env.s = newTestSettle(env.c, tracker, cf)
			gen := env.s.term.Begin("1")
			if tc.arm != nil {
				tc.arm(env)
			}

			var res dispatch.Result
			switch {
			case tc.result != nil:
				res = tc.result(env)
			case tc.entry == entryRelayed:
				res = selfReportReady()
			default:
				res = settleReadyResult("1", env.landing)
			}
			res.Passes = env.passes
			var got Settlement
			switch tc.entry {
			case entryRelayed:
				st, err := env.s.Recover(RecoverInput{
					Num: "1", Resolved: res.Resolved, Passes: res.Passes,
					Begin: func() (dispatch.Dispatcher, error) { return dispatch.NewFake(), nil },
				})
				got = st
				if err != nil {
					t.Errorf("Recover err = %v, want nil", err)
				}
			case entryAdopted:
				got = env.s.SettleAdopted(dispatch.NewFake(), "1", gen, pushOnlyBranch)
			default:
				got = env.s.Settle(dispatch.NewFake(), "1", gen, res)
			}

			iss, _ := fc.Issue("1")
			if got != tc.want {
				t.Errorf("Settle = %+v, want %+v", got, tc.want)
			}
			if tc.abandoned {
				assertNoTransitionOrComment(t, fc)
				assertClaimUntouched(t, fc)
			}
			if missing := containsSteps(rec.Steps(), tc.wantSteps); len(missing) > 0 {
				t.Errorf("progress steps = %+v, missing %+v", rec.Steps(), missing)
			}
			for _, l := range tc.wantLabels {
				if !containsLabel(iss.Labels, l) {
					t.Errorf("issue must carry %s; labels=%v", l, iss.Labels)
				}
			}
			for _, l := range tc.wantNoLabels {
				if containsLabel(iss.Labels, l) {
					t.Errorf("issue must not carry %s; labels=%v", l, iss.Labels)
				}
			}
			if fc.Merged != tc.wantMerged {
				t.Errorf("fc.Merged = %q, want %q", fc.Merged, tc.wantMerged)
			}
			if len(fc.RelayBundleCalls) != tc.wantRelays {
				t.Errorf("RelayBundle calls = %+v, want %d", fc.RelayBundleCalls, tc.wantRelays)
			}
			if len(fc.RebasedURLs) != tc.wantRebases {
				t.Errorf("Rebase calls = %v, want %d", fc.RebasedURLs, tc.wantRebases)
			}
			var bodies []string
			for _, cc := range fc.CommentCalls {
				if cc.Body != "" { // postUsageComment adds an empty one under dispatch.NewFake
					bodies = append(bodies, cc.Body)
				}
			}
			if len(bodies) != len(tc.wantComments) {
				t.Errorf("comments = %q, want %d containing %q", bodies, len(tc.wantComments), tc.wantComments)
			} else {
				for i, w := range tc.wantComments {
					if !strings.Contains(bodies[i], w) {
						t.Errorf("comment %d = %q, want it to contain %q", i, bodies[i], w)
					}
				}
			}
			if tc.bundle {
				if tc.wantBundle == bundleUnset {
					t.Fatal("bundle row must set wantBundle")
				}
				if gone := !bundleExists(t, env.outbox); gone != (tc.wantBundle == bundleDropped) {
					t.Errorf("bundle removed = %v, want fate %v", gone, tc.wantBundle)
				}
			}
			if tc.wantLandings != nil && !reflect.DeepEqual(fc.RecordLandingCalls, tc.wantLandings) {
				t.Errorf("RecordLanding calls = %+v, want %+v", fc.RecordLandingCalls, tc.wantLandings)
			}
			if tc.wantPasses != nil && !reflect.DeepEqual(fc.RecordLandingPassCalls, tc.wantPasses) {
				t.Errorf("RecordLandingPass calls = %+v, want %+v", fc.RecordLandingPassCalls, tc.wantPasses)
			}
			if tc.check != nil {
				tc.check(t, env)
			}
		})
	}
}
