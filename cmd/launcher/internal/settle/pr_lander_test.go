package settle

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/testutil"
)

// prShape picks the capability shape the tracker and Code Forge fakes take,
// which is what steers New toward the PR Lander.
type prShape int

const (
	shapeGithub   prShape = iota // the bare fake: a PR forge plus every optional tracker capability
	shapeForgejo                 // forgejo's tracker (no LandingRecorder) over a PR forge
	shapeLocalPR                 // local's tracker (LandingRecorder) paired with a PR forge
	shapeReadOnly                // the bare tracker over a read-only Box's PR forge, which relays a bundle in

	shapeGithubReadOnly // github's tracker (no LandingRecorder) over a read-only Box's PR forge, so its PRs carry a Closes reference
	shapeLocalReadOnly  // local's tracker over a read-only Box's PR forge
)

func (sh prShape) build(fc *forge.Fake) (forge.IssueTracker, forge.CodeForge) {
	switch sh {
	case shapeForgejo:
		return fc.AsForgejoShaped(), fc
	case shapeLocalPR:
		return fc.AsLocalShaped(), fc
	case shapeGithubReadOnly:
		return fc.AsNoLandingRecorder(), fc.AsGithubReadOnly()
	case shapeLocalReadOnly:
		return fc.AsLocalShaped(), fc.AsGithubReadOnly()
	case shapeReadOnly:
		return fc, fc.AsGithubReadOnly()
	}
	return fc, fc
}

// prCase is one PR landing of a green PR observed through Settle's public
// entry point: the Settlement, the progress steps, the tracker's labels and
// the side effects the Lander owns on the forge fake.
type prCase struct {
	name      string
	entry     pushOnlyEntry    // which public entry reaches the landing; default Settle
	noPR      bool             // no PR on the agent branch going in: the Box's hand-off opens it
	result    *dispatch.Result // the Box outcome Settle is handed; default a ready outcome on testPR
	shape     prShape
	tracker   backend.Descriptor // the tracker row Settle reasons about; zero when the row doesn't care
	mergeMode string             // default immediate
	guard     string             // MERGE_GUARD_PATHS
	labels    []string           // the issue's labels going in; default agent-in-progress
	body      string             // the issue's body going in
	files     []string           // the PR's changed files; default src/main.go
	setup     func(*testing.T, *forge.Fake, *Config)

	maxRebase  int                               // MAX_REBASE_ATTEMPTS
	maxFix     int                               // MAX_FIX_ATTEMPTS
	checks     []forge.RollupState               // successive CI rollups; default green twice
	mergeErrs  []error                           // successive Merge results, consumed before it succeeds
	rebaseErr  error                             // what Rebase returns
	mergeable  forge.MergeableState              // what the forge reports for the PR's mergeability
	resolveErr error                             // what the conflict-resolve Box returns
	dsetup     func(*dispatch.Fake, *forge.Fake) // scripts the dispatcher beyond resolveErr
	afterMerge func(*forge.Fake)                 // runs on the fake right after a successful Merge
	check      func(*testing.T, *forge.Fake, *dispatch.Fake)
	wire       func(fc *forge.Fake, d *dispatch.Fake, mark func()) // runs once Settle exists; mark is the operator's Terminate landing mid-call

	want           Settlement
	wantSteps      []Step
	wantNoSteps    []Step   // steps Settle must not have recorded
	wantNoStatus   []Status // statuses Settle must not have recorded, whatever their detail
	wantLabels     []string
	wantNoLabels   []string
	wantMerged     string   // the PR Merge was called on
	wantCalls      []string // forge.Fake.LandingCallLog: MarkReady/EnqueueAutoMerge/Merge in order
	wantComments   []string // substring of each issue comment, in order; none when nil
	wantPRComments []string // substring of each PR comment, in order; unchecked when nil
	wantPRExact    string   // the sole PR comment, byte for byte
	abandoned      bool     // a stop left the released issue untouched; want is the zero Settlement
	prClosed       bool     // the settled reason leaves no open PR although nothing merged
	wantFixes      int      // fix passes dispatched
	wantResolves   int      // conflict-resolve Boxes dispatched
	wantRebases    int      // Rebase calls on the forge
	wantStderr     []string // substring each of stderr must hold; non-nil empty means silent; stderr uncaptured when nil
	wantNoStderr   []string // substring stderr must not hold
	wantErr        []error  // an entryRelayed Recover's error must wrap each of these; nil error otherwise
	wantNoErr      []error  // an entryRelayed Recover's error must wrap none of these
	wantClosed     []string // MergeCloser's CloseMergedIssueCalls; unchecked when nil, non-nil empty means none
	wantStepsLike  []Step   // steps by Key, Landing, Status and a Detail substring
}

// prSettlement is the Settlement a PR landing of testPR settles with.
func prSettlement(reason string) Settlement {
	return Settlement{Verdict: ForReason(reason), PRURL: testPR}
}

func guardWording(path string) string {
	return "merge guard: MERGE_GUARD_PATHS matched " + path + " — downgrading this merge to manual regardless of MERGE_MODE; review and merge by hand"
}

func TestSettle_PRLander(t *testing.T) {
	forReason := func(r string) Settlement { return Settlement{Verdict: ForReason(r), PRURL: testPR} }
	merged := forReason(dispatchrecord.ReasonMerged)
	manual := forReason(dispatchrecord.ReasonManual)
	autoEnqueued := forReason(dispatchrecord.ReasonAutoMergeEnqueued)
	mergeBlocked := forReason(dispatchrecord.ReasonMergeBlocked)
	guardHit := forReason(dispatchrecord.ReasonMergeGuardHit)
	checkError := forReason(dispatchrecord.ReasonMergeGuardCheckError)
	byHand := forReason(dispatchrecord.ReasonMergeByHand)
	complete := []string{"agent-complete"}
	noFailed := []string{"agent-failed"}
	tuningLabels := []string{"agent-in-progress", "agent-tuning-finding"}
	flipThenMerge := []string{"MarkReady:" + testPR, "Merge:" + testPR}
	flipOnly := []string{"MarkReady:" + testPR}
	errGuardList := errors.New("gh api pulls files: 403 Forbidden")
	errIssueRead := errors.New("gh issue view: 502")
	errCommentPR := errors.New("gh pr comment: 502")
	holdWording := "merge held for tuning provenance: this PR closes a tuning finding (agent-tuning-finding) — not merging regardless of MERGE_MODE; review and merge by hand. Removing the agent-tuning-finding label from the issue is the override and lets a PR merge normally"
	errWording := "tuning provenance: could not read the closing issue (boom) — downgrading to manual as a precaution; review and merge by hand"

	cases := []prCase{
		{
			name: "merge failure after green keeps the issue complete",
			setup: func(_ *testing.T, fc *forge.Fake, c *Config) {
				c.MaxRebaseAttempts = 0
				fc.MergeErr = errors.New("required review missing")
			},
			want: mergeBlocked, wantLabels: complete, wantNoLabels: noFailed, wantCalls: flipThenMerge,
			wantComments: []string{"merge blocked after green CI: required review missing"},
			wantSteps:    []Step{{Key: "1", Landing: testPR, Status: StatusMergeBlocked, Detail: "!! required review missing"}},
		},
		{
			name: "green PR is marked ready before it merges",
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge,
		},
		{
			name: "mark-ready failure does not block the merge",
			setup: func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.MarkReadyErr = errors.New("HTTP 403: Resource not accessible by integration")
			},
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge,
		},
		{
			name: "auto mode marks ready before enqueueing the auto-merge", mergeMode: "auto",
			want: autoEnqueued, wantLabels: complete, wantCalls: []string{"MarkReady:" + testPR, "EnqueueAutoMerge:" + testPR},
		},
		{
			name: "manual mode still flips the PR ready and leaves it unmerged", mergeMode: "manual",
			want: manual, wantLabels: complete, wantCalls: flipOnly,
		},
		{
			name: "merge guard hit downgrades to manual", guard: ".github/**,**/CLAUDE.md",
			files: []string{"src/main.go", ".github/workflows/ci.yml"},
			want:  guardHit, wantLabels: complete, wantNoLabels: noFailed, wantCalls: flipOnly,
			wantPRExact: guardWording(".github/workflows/ci.yml"),
		},
		{
			name: "merge guard hit on a forgejo workflow path", shape: shapeForgejo, tracker: backend.Forgejo,
			guard: ".github/**,.forgejo/**", files: []string{"src/main.go", ".forgejo/workflows/ci.yml"},
			want: guardHit, wantLabels: complete, wantCalls: flipOnly,
			wantPRExact: guardWording(".forgejo/workflows/ci.yml"),
		},
		{
			name: "merge guard hit under auto mode never enqueues", mergeMode: "auto", guard: ".github/**",
			files: []string{".github/workflows/ci.yml"},
			want:  guardHit, wantLabels: complete, wantCalls: flipOnly,
			wantPRExact: guardWording(".github/workflows/ci.yml"),
		},
		{
			name: "a failed PR comment does not lift the merge guard", guard: ".github/**",
			files: []string{".github/workflows/ci.yml"},
			setup: func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.CommentPRErr = errCommentPR },
			want:  guardHit, wantLabels: complete, wantCalls: flipOnly,
		},
		{
			name: "merge guard miss merges normally", guard: ".github/**,**/CLAUDE.md",
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge, wantPRComments: []string{},
		},
		{
			name: "merge guard check error fails safe", guard: ".github/**",
			setup: func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.PRFilesErr = errGuardList },
			want:  checkError, wantLabels: complete, wantNoLabels: noFailed, wantCalls: flipOnly,
			wantPRComments: []string{"precaution"},
		},
		{
			name: "tuning label holds the immediate merge", tracker: backend.GitHub, labels: tuningLabels,
			want: byHand, wantLabels: complete, wantNoLabels: noFailed, wantCalls: flipOnly, wantPRExact: holdWording,
		},
		{
			name: "tuning label also holds on local", shape: shapeLocalPR, tracker: backend.Local, labels: tuningLabels,
			want: byHand, wantLabels: complete, wantNoLabels: noFailed, wantCalls: flipOnly, wantPRExact: holdWording,
		},
		{
			name: "tuning label holds despite a merge guard miss", tracker: backend.GitHub, labels: tuningLabels, guard: ".github/**",
			want: byHand, wantLabels: complete, wantCalls: flipOnly, wantPRExact: holdWording,
		},
		{
			name: "a failed PR comment does not lift the tuning hold", tracker: backend.GitHub, labels: tuningLabels,
			setup: func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.CommentPRErr = errCommentPR },
			want:  byHand, wantLabels: complete, wantCalls: flipOnly,
		},
		{
			name: "an unreadable closing issue fails safe", tracker: backend.GitHub,
			setup: func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.IssueErr = errors.New("boom") },
			want:  checkError, wantLabels: complete, wantNoLabels: noFailed, wantCalls: flipOnly, wantPRExact: errWording,
		},
		{
			name: "tuning marker without its label does not hold on local", shape: shapeLocalPR, tracker: backend.Local,
			body: "plain body\n<!-- spindrift-dedup: foo, chore=docs -->",
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge, wantPRComments: []string{},
		},
		{
			name: "synthetic merge-by-hand row holds by its label", tracker: backend.GitHub,
			labels: []string{"agent-in-progress", "agent-synthetic-finding"},
			setup:  func(t *testing.T, _ *forge.Fake, _ *Config) { withCatalogRows(t, syntheticHold, syntheticFree) },
			want:   byHand, wantLabels: complete, wantCalls: flipOnly, wantPRComments: []string{"agent-synthetic-finding"},
		},
		{
			name: "synthetic merge-by-hand row holds by its marker on local", shape: shapeLocalPR, tracker: backend.Local,
			body:  "finding\n<!-- spindrift-dedup: foo, chore=synthetic -->",
			setup: func(t *testing.T, _ *forge.Fake, _ *Config) { withCatalogRows(t, syntheticHold) },
			want:  byHand, wantLabels: complete, wantCalls: flipOnly, wantPRComments: []string{"agent-synthetic-finding"},
		},
		{
			name: "the tuning marker is no hold once tuning is off the catalog", shape: shapeLocalPR, tracker: backend.Local,
			body:  tuningBody,
			setup: func(t *testing.T, _ *forge.Fake, _ *Config) { withCatalogRows(t, syntheticHold) },
			want:  merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge, wantPRComments: []string{},
		},
		{
			// Without a merge-by-hand row the closing issue is never even read.
			name: "a non-merge-by-hand row never reads the closing issue", tracker: backend.GitHub,
			labels: []string{"agent-in-progress", "agent-freebie-finding"},
			setup: func(t *testing.T, fc *forge.Fake, _ *Config) {
				withCatalogRows(t, syntheticFree)
				fc.IssueErr = errIssueRead
			},
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge, wantPRComments: []string{},
		},
	}

	// Removing the label is the override: where issues wear labels the body
	// marker alone must not hold, Jira included though it has no label registry.
	for _, tc := range []struct {
		shape   prShape
		tracker backend.Descriptor
	}{{shapeGithub, backend.GitHub}, {shapeForgejo, backend.Forgejo}, {shapeGithub, backend.Jira}} {
		cases = append(cases, prCase{
			name: "tuning marker alone does not hold on " + tc.tracker.Name, shape: tc.shape, tracker: tc.tracker, body: tuningBody,
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge, wantPRComments: []string{},
		})
	}

	// Every MERGE_MODE holds a tuning PR, by label where the tracker has a
	// label registry and by the chore=tuning marker where it has none: the
	// issue ends agent-complete and the PR is flipped ready, never merged.
	for _, mode := range []string{"immediate", "auto", "manual"} {
		for _, tc := range []struct {
			name    string
			shape   prShape
			tracker backend.Descriptor
			labels  []string
			body    string
		}{
			{"label on github", shapeGithub, backend.GitHub, tuningLabels, ""},
			{"label on jira", shapeGithub, backend.Jira, tuningLabels, ""},
			{"marker on local", shapeLocalPR, backend.Local, nil, tuningBody},
		} {
			cases = append(cases, prCase{
				name: "tuning hold under " + mode + " mode: " + tc.name, shape: tc.shape, tracker: tc.tracker,
				mergeMode: mode, labels: tc.labels, body: tc.body,
				want: byHand, wantLabels: complete, wantNoLabels: noFailed, wantCalls: flipOnly, wantPRExact: holdWording,
			})
		}
	}

	cases = append(cases, healCases()...)
	cases = append(cases, handOffCases()...)
	cases = append(cases, fixCases()...)
	cases = append(cases, mergeCases()...)
	cases = append(cases, verifyCases()...)
	cases = append(cases, terminateCases()...)
	cases = append(cases, relayCases()...)
	cases = append(cases, draftPRCases()...)
	cases = append(cases, bundleCases()...)
	cases = append(cases, adoptCases()...)
	cases = append(cases, closeCases()...)
	cases = append(cases, entryCases()...)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig()
			c.MergeMode = "immediate"
			if tc.mergeMode != "" {
				c.MergeMode = tc.mergeMode
			}
			c.MergeGuardPaths = tc.guard
			c.MaxRebaseAttempts = tc.maxRebase
			c.MaxFixAttempts = tc.maxFix
			rec := &ProgressRecorder{}
			c.Progress = rec
			fc := forge.NewFake(testDispatchLabels)
			labels := []string{"agent-in-progress"}
			if tc.labels != nil {
				labels = tc.labels
			}
			fc.SetIssue(forge.Issue{Number: "1", Labels: labels, Body: tc.body})
			if !tc.noPR {
				fc.SetPR(fc.AgentBranch("1"), forge.PR{URL: testPR})
			}
			fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
			files := []string{"src/main.go"}
			if tc.files != nil {
				files = tc.files
			}
			fc.SetPRFiles(testPR, files)
			if tc.checks != nil {
				fc.SetCheckStates(testPR, tc.checks)
			}
			fc.MergeErrs = tc.mergeErrs
			fc.RebaseErr = tc.rebaseErr
			if tc.mergeable != "" {
				fc.SetMergeableState(testPR, tc.mergeable)
			}
			d := dispatch.NewFake()
			d.ResolveConflictErr = tc.resolveErr
			if tc.dsetup != nil {
				tc.dsetup(d, fc)
			}
			if tc.setup != nil {
				tc.setup(t, fc, &c)
			}
			tracker, cf := tc.shape.build(fc)
			if tc.afterMerge != nil {
				cf = readErrAfterMerge{fc, tc.afterMerge}
			}
			s := newTestSettle(c, tracker, cf)
			s.cfg.Capabilities.TrackerDescriptor = tc.tracker
			// Recover settles at RecoverGen and takes no Begin, so a stop marks that generation.
			gen := RecoverGen
			if tc.entry == entrySettle {
				gen = s.term.Begin("1")
			}
			begin := func() (dispatch.Dispatcher, error) { return d, nil }

			if tc.wire != nil {
				tc.wire(fc, d, func() { s.term.Mark("1") })
			}
			var got Settlement
			result := settleReadyResult("1", testPR)
			if tc.entry == entryRelayed {
				result = selfReportReady()
			}
			if tc.result != nil {
				result = *tc.result
			}
			settle := func() {
				switch tc.entry {
				case entryAdopted:
					st, err := s.Recover(RecoverInput{Num: "1", OpenPR: testPR, Begin: begin})
					got = st
					if err != nil {
						t.Errorf("Recover err = %v, want nil", err)
					}
				case entryRelayed:
					st, err := s.Recover(RecoverInput{Num: "1", Resolved: result.Resolved, Passes: result.Passes, Begin: begin})
					got = st
					if (err != nil) != (len(tc.wantErr) > 0) {
						t.Errorf("Recover err = %v; want wrapping %v", err, tc.wantErr)
					}
					for _, w := range tc.wantErr {
						if !errors.Is(err, w) {
							t.Errorf("Recover err = %v, want it to wrap %v", err, w)
						}
					}
					for _, w := range tc.wantNoErr {
						if errors.Is(err, w) {
							t.Errorf("Recover err = %v, want it free of %v", err, w)
						}
					}
				default:
					got = s.Settle(d, "1", gen, result)
				}
			}
			var stderr string
			if tc.wantStderr != nil || tc.wantNoStderr != nil {
				stderr = testutil.CaptureStderr(t, settle)
			} else {
				settle()
			}

			fc.IssueErr = nil
			iss, _ := fc.Issue("1")
			if got != tc.want {
				t.Errorf("Settle = %+v, want %+v", got, tc.want)
			}
			if tc.abandoned {
				assertNoTransitionOrComment(t, fc)
				assertClaimUntouched(t, fc)
			} else if open := dispatchrecord.ReasonLeavesPROpen(got.Reason()); open != (tc.wantMerged == "" && !tc.prClosed) {
				t.Errorf("reason %q leaves the PR open = %v, want %v", got.Reason(), open, !open)
			}
			if len(d.FixCalls) != tc.wantFixes {
				t.Errorf("fix passes = %+v, want %d", d.FixCalls, tc.wantFixes)
			}
			if len(d.ResolveConflictCalls) != tc.wantResolves {
				t.Errorf("conflict-resolves = %+v, want %d", d.ResolveConflictCalls, tc.wantResolves)
			}
			if len(fc.RebasedURLs) != tc.wantRebases {
				t.Errorf("Rebase calls = %v, want %d", fc.RebasedURLs, tc.wantRebases)
			}
			if tc.check != nil {
				tc.check(t, fc, d)
			}
			if tc.wantStderr != nil && len(tc.wantStderr) == 0 && stderr != "" {
				t.Errorf("stderr = %q, want it silent", stderr)
			}
			for _, w := range tc.wantStderr {
				if !strings.Contains(stderr, w) {
					t.Errorf("stderr = %q, want it to contain %q", stderr, w)
				}
			}
			for _, w := range tc.wantNoStderr {
				if strings.Contains(stderr, w) {
					t.Errorf("stderr = %q, want it free of %q", stderr, w)
				}
			}
			if missing := containsSteps(rec.Steps(), tc.wantSteps); len(missing) > 0 {
				t.Errorf("progress steps = %+v, missing %+v", rec.Steps(), missing)
			}
			for _, w := range tc.wantStepsLike {
				if !slices.ContainsFunc(rec.Steps(), func(g Step) bool {
					return g.Key == w.Key && g.Landing == w.Landing && g.Status == w.Status && strings.Contains(g.Detail, w.Detail)
				}) {
					t.Errorf("progress steps = %+v, missing one like %+v", rec.Steps(), w)
				}
			}
			for _, st := range tc.wantNoStatus {
				if slices.ContainsFunc(rec.Steps(), func(g Step) bool { return g.Status == st }) {
					t.Errorf("progress steps = %+v, want none with status %q", rec.Steps(), st)
				}
			}
			for _, st := range tc.wantNoSteps {
				if slices.Contains(rec.Steps(), st) {
					t.Errorf("progress steps = %+v, want no %+v", rec.Steps(), st)
				}
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
			if tc.wantClosed != nil && !slices.Equal(fc.CloseMergedIssueCalls, tc.wantClosed) {
				t.Errorf("CloseMergedIssueCalls = %v, want %v", fc.CloseMergedIssueCalls, tc.wantClosed)
			}
			if fc.Merged != tc.wantMerged {
				t.Errorf("fc.Merged = %q, want %q", fc.Merged, tc.wantMerged)
			}
			if !slices.Equal(fc.LandingCallLog, tc.wantCalls) {
				t.Errorf("landing calls = %v, want %v", fc.LandingCallLog, tc.wantCalls)
			}
			var bodies []string
			for _, cc := range fc.CommentCalls {
				if cc.Body != "" { // postUsageComment adds an empty one under dispatch.NewFake
					bodies = append(bodies, cc.Body)
				}
			}
			if len(bodies) != len(tc.wantComments) {
				t.Errorf("issue comments = %q, want %d containing %q", bodies, len(tc.wantComments), tc.wantComments)
			} else {
				for i, w := range tc.wantComments {
					if !strings.Contains(bodies[i], w) {
						t.Errorf("issue comment %d = %q, want it to contain %q", i, bodies[i], w)
					}
				}
			}
			var prBodies []string
			for _, cc := range fc.CommentPRCalls {
				if cc.PR != testPR {
					t.Errorf("PR comment on %q, want %q", cc.PR, testPR)
				}
				prBodies = append(prBodies, cc.Body)
			}
			if tc.wantPRExact != "" {
				tc.wantPRComments = []string{tc.wantPRExact}
			}
			if tc.wantPRComments != nil && len(prBodies) != len(tc.wantPRComments) {
				t.Errorf("PR comments = %q, want %d containing %q", prBodies, len(tc.wantPRComments), tc.wantPRComments)
			} else {
				for i, w := range tc.wantPRComments {
					if tc.wantPRExact != "" && prBodies[i] != w || tc.wantPRExact == "" && !strings.Contains(prBodies[i], w) {
						t.Errorf("PR comment %d = %q, want %q", i, prBodies[i], w)
					}
				}
			}
		})
	}
}

// recoverOpenPR settles num's already-discovered open PR through Recover with
// d as the claimed Dispatch, the way spindrift recover reaches the adopt gate.
func recoverOpenPR(t *testing.T, s *Settle, d dispatch.Dispatcher, num, prURL string) Settlement {
	t.Helper()
	st, err := s.Recover(RecoverInput{
		Num: num, OpenPR: prURL,
		Begin: func() (dispatch.Dispatcher, error) { return d, nil },
	})
	if err != nil {
		t.Fatalf("Recover err = %v, want nil", err)
	}
	return st
}
