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
)

// prShape picks the capability shape the tracker and Code Forge fakes take,
// which is what steers New toward the PR Lander.
type prShape int

const (
	shapeGithub  prShape = iota // the bare fake: a PR forge plus every optional tracker capability
	shapeForgejo                // forgejo's tracker (no LandingRecorder) over a PR forge
	shapeLocalPR                // local's tracker (LandingRecorder) paired with a PR forge
)

func (sh prShape) build(fc *forge.Fake) (forge.IssueTracker, forge.CodeForge) {
	switch sh {
	case shapeForgejo:
		return fc.AsForgejoShaped(), fc
	case shapeLocalPR:
		return fc.AsLocalShaped(), fc
	}
	return fc, fc
}

// prCase is one PR landing of a green PR observed through Settle's public
// entry point: the Settlement, the progress steps, the tracker's labels and
// the side effects the Lander owns on the forge fake.
type prCase struct {
	name      string
	shape     prShape
	tracker   backend.Descriptor // the tracker row Settle reasons about; zero when the row doesn't care
	mergeMode string             // default immediate
	guard     string             // MERGE_GUARD_PATHS
	labels    []string           // the issue's labels going in; default agent-in-progress
	body      string             // the issue's body going in
	files     []string           // the PR's changed files; default src/main.go
	setup     func(*testing.T, *forge.Fake, *Config)

	want           Settlement
	wantSteps      []Step
	wantLabels     []string
	wantNoLabels   []string
	wantMerged     string   // the PR Merge was called on
	wantCalls      []string // forge.Fake.LandingCallLog: MarkReady/EnqueueAutoMerge/Merge in order
	wantComments   []string // substring of each issue comment, in order; none when nil
	wantPRComments []string // substring of each PR comment, in order; unchecked when nil
	wantPRExact    string   // the sole PR comment, byte for byte
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

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig()
			c.MergeMode = "immediate"
			if tc.mergeMode != "" {
				c.MergeMode = tc.mergeMode
			}
			c.MergeGuardPaths = tc.guard
			rec := &ProgressRecorder{}
			c.Progress = rec
			fc := forge.NewFake(testDispatchLabels)
			labels := []string{"agent-in-progress"}
			if tc.labels != nil {
				labels = tc.labels
			}
			fc.SetIssue(forge.Issue{Number: "1", Labels: labels, Body: tc.body})
			fc.SetPR(fc.AgentBranch("1"), forge.PR{URL: testPR})
			fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
			files := []string{"src/main.go"}
			if tc.files != nil {
				files = tc.files
			}
			fc.SetPRFiles(testPR, files)
			if tc.setup != nil {
				tc.setup(t, fc, &c)
			}
			tracker, cf := tc.shape.build(fc)
			s := newTestSettle(c, tracker, cf)
			s.cfg.Capabilities.TrackerDescriptor = tc.tracker
			gen := s.term.Begin("1")

			got := s.Settle(dispatch.NewFake(), "1", gen, settleReadyResult("1", testPR))

			fc.IssueErr = nil
			iss, _ := fc.Issue("1")
			if got != tc.want {
				t.Errorf("Settle = %+v, want %+v", got, tc.want)
			}
			if open := dispatchrecord.ReasonLeavesPROpen(got.Reason()); open != (tc.wantMerged == "") {
				t.Errorf("reason %q leaves the PR open = %v, want %v", got.Reason(), open, tc.wantMerged == "")
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
