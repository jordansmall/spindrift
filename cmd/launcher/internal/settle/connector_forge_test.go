package settle

import (
	"slices"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/connectorforge/connectorforgetest"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
)

// A handshake declaring push-only resolves to a push-only forge, and the
// push-only Lander settles through it: the merge runs in core's git plumbing
// against a real bare repo, not through a PR.
func TestSettle_PushOnlyLanderThroughConnectorForge(t *testing.T) {
	h := connectorforgetest.New(t)
	h.SeedLandable("1")

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	cfg := baseConfig()
	cfg.Capabilities = h.Capabilities()
	s := New(cfg, fc, h.Forge())

	if _, ok := s.lander.(pushOnlyLander); !ok {
		t.Fatalf("lander = %T, want pushOnlyLander for a push-only handshake", s.lander)
	}

	gen := s.term.Begin("1")
	got := s.Settle(dispatch.NewFake(), "1", gen, settleReadyResult("1", h.BranchPrefix()+"1"))

	if want := (Settlement{Verdict: ForReason(dispatchrecord.ReasonMerged)}); got != want {
		t.Errorf("Settle = %+v, want %+v", got, want)
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete; labels=%v", iss.Labels)
	}
	if !h.Landed("1") {
		t.Error("agent branch did not reach main on the bare repo")
	}
}

// Every MERGE_MODE settles through a Connector-backed PR forge exactly as it
// does through a native one: the PR Lander is chosen, and the backend sees the
// merge, the auto-merge enqueue, or nothing, per mode. A Connector without
// "forge.autoMerge" cannot enqueue, so core's auto path blocks the merge with a
// comment and leaves the PR open, as for a native forge whose CanAutoMerge is
// false (the launcher preflight refuses that combination before dispatch).
func TestSettle_PRLanderThroughConnectorForge(t *testing.T) {
	merged := Settlement{Verdict: ForReason(dispatchrecord.ReasonMerged)}
	for _, tc := range []struct {
		name          string
		wasm          string
		mode          string
		want          func(url string) Settlement
		wantMerged    bool
		wantEnqueued  bool
		wantLanded    bool
		wantCanAuto   bool
		wantStatus    Status
		wantComment   string
		wantMergeReqs int
	}{
		{
			name: "immediate merges the PR", wasm: connectorforgetest.PRWasm, mode: "immediate",
			want:       func(url string) Settlement { return Settlement{Verdict: merged.Verdict, PRURL: url} },
			wantMerged: true, wantLanded: true, wantCanAuto: true, wantMergeReqs: 1,
		},
		{
			name: "auto enqueues without merging", wasm: connectorforgetest.PRWasm, mode: "auto",
			want: func(url string) Settlement {
				return Settlement{Verdict: ForReason(dispatchrecord.ReasonAutoMergeEnqueued), PRURL: url}
			},
			wantEnqueued: true, wantCanAuto: true, wantMergeReqs: 1, wantStatus: StatusAutoMergeEnqueued,
		},
		{
			name: "manual leaves the PR open and ready", wasm: connectorforgetest.PRWasm, mode: "manual",
			want: func(url string) Settlement {
				return Settlement{Verdict: ForReason(dispatchrecord.ReasonManual), PRURL: url}
			},
			wantCanAuto: true, wantStatus: StatusAgentComplete,
		},
		{
			name: "immediate without forge.ciRun merges with no run link", wasm: connectorforgetest.MinimalPRWasm, mode: "immediate",
			want:       func(url string) Settlement { return Settlement{Verdict: merged.Verdict, PRURL: url} },
			wantMerged: true, wantLanded: true, wantMergeReqs: 1,
		},
		{
			name: "auto without forge.autoMerge blocks the merge", wasm: connectorforgetest.MinimalPRWasm, mode: "auto",
			want: func(url string) Settlement {
				return Settlement{Verdict: ForReason(dispatchrecord.ReasonMergeBlocked), PRURL: url}
			},
			wantStatus: StatusAutoMergeEnqueueFailed, wantComment: "auto-merge enqueue failed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := connectorforgetest.NewPRVariant(t, tc.wasm)
			url := h.SeedLandablePR("1")
			// Allowed on the backend, so the minimal row proves the missing
			// capability, not the repo setting, is what disables auto-merge.
			h.Backend.SetAutoMergeAllowed(true)

			fc := forge.NewFake(testDispatchLabels)
			fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
			rec := &ProgressRecorder{}
			cfg := baseConfig()
			cfg.MergeMode = tc.mode
			cfg.Progress = rec
			cfg.Capabilities = h.Capabilities()
			s := New(cfg, fc, h.CodeForge())

			if _, ok := s.lander.(prLander); !ok {
				t.Fatalf("lander = %T, want prLander for a PR-capable handshake", s.lander)
			}
			if canAuto, err := h.Forge().CanAutoMerge(); err != nil || canAuto != tc.wantCanAuto {
				t.Errorf("CanAutoMerge = (%v, %v), want (%v, nil)", canAuto, err, tc.wantCanAuto)
			}

			gen := s.term.Begin("1")
			got := s.Settle(dispatch.NewFake(), "1", gen, settleReadyResult("1", url))

			if want := tc.want(url); got != want {
				t.Errorf("Settle = %+v, want %+v", got, want)
			}
			if h.Merged(url) != tc.wantMerged {
				t.Errorf("PR merged = %v, want %v", h.Merged(url), tc.wantMerged)
			}
			if h.AutoMergeEnqueued(url) != tc.wantEnqueued {
				t.Errorf("auto-merge enqueued = %v, want %v", h.AutoMergeEnqueued(url), tc.wantEnqueued)
			}
			if h.Landed("1") != tc.wantLanded {
				t.Errorf("branch landed = %v, want %v", h.Landed("1"), tc.wantLanded)
			}
			if h.IsDraft(url) {
				t.Error("PR left draft; want ready")
			}
			if got := h.Backend.MergeRequests(); got != tc.wantMergeReqs {
				t.Errorf("backend saw %d merge requests, want %d", got, tc.wantMergeReqs)
			}
			iss, _ := fc.Issue("1")
			if !containsLabel(iss.Labels, "agent-complete") {
				t.Errorf("issue must carry agent-complete; labels=%v", iss.Labels)
			}
			if tc.wantStatus != detailOnly && !slices.ContainsFunc(rec.Steps(), func(st Step) bool { return st.Status == tc.wantStatus }) {
				t.Errorf("no %q progress step; steps = %+v", tc.wantStatus, rec.Steps())
			}
			var gotComment string
			for _, c := range fc.CommentCalls {
				gotComment += c.Body
			}
			if tc.wantComment == "" && gotComment != "" || !strings.Contains(gotComment, tc.wantComment) {
				t.Errorf("comments = %+v, want one containing %q", fc.CommentCalls, tc.wantComment)
			}
		})
	}
}

// readOnlySettle settles issue 1's ready hand-off for a read-only Box whose
// bundle sits in outbox. A nil cf is the tracker Fake itself, with the
// capabilities it resolves: a PR forge with no BundleRelay, the native shape a
// Connector without forge.draftPRs is held to (caps is then ignored).
func readOnlySettle(t *testing.T, caps forge.Capabilities, cf forge.CodeForge, outbox string, maxRetries int) (Settlement, *forge.Fake, *ProgressRecorder) {
	t.Helper()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	if cf == nil {
		cf = fc
		caps = forge.ResolveCapabilities(cf, fc, backend.Descriptor{}, githubTrackerRow)
	}
	rec := &ProgressRecorder{}
	cfg := baseConfig()
	cfg.Capabilities = caps
	cfg.ReadOnly, cfg.BaseBranch, cfg.Progress = true, "main", rec
	cfg.OutboxDir = func(string) string { return outbox }
	cfg.Policy = retry.Policy{Max: maxRetries, Unit: time.Second}
	s := New(cfg, fc, cf)
	gen := s.term.Begin("1")
	return s.Settle(dispatch.NewFake(), "1", gen, *handoffResult("ready")), fc, rec
}

func statusesOf(rec *ProgressRecorder, among ...Status) []Status {
	var got []Status
	for _, st := range rec.Steps() {
		if slices.Contains(among, st.Status) {
			got = append(got, st.Status)
		}
	}
	return got
}

// A draft-PR create the forge refuses is retried, looked up once more, then
// parked agent-failed with the relayed bundle still in the outbox, exactly as
// for a native forge (issue #5144; TestSettle_PRLander pins the same schedule
// via draftPRCases): the Connector relays in core's git, so the
// branch is on the remote before the create is attempted.
func TestSettle_FailedDraftPRCreateParksThroughConnectorForge(t *testing.T) {
	parked := Settlement{Verdict: ForReason(dispatchrecord.ReasonDraftPRFailed), Note: "draft PR create failed; work preserved in outbox"}
	for _, tc := range []struct {
		name    string
		retries int
		want    []Status
	}{
		{"with a retry budget", 2, []Status{StatusDraftPRRetry, StatusDraftPRRetry, StatusDraftPRFinalLookup, StatusDraftPRFailed}},
		{"with no retry budget", 0, []Status{StatusDraftPRFinalLookup, StatusDraftPRFailed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := connectorforgetest.NewHostMediation(t)
			outbox := h.SeedBundle(handoffBranch)
			h.Backend.RefuseHead(handoffBranch)

			got, fc, rec := readOnlySettle(t, h.Capabilities(), h.CodeForge(), outbox, tc.retries)

			if got != parked {
				t.Errorf("Settle = %+v, want %+v", got, parked)
			}
			if !h.BundleLanded(handoffBranch) {
				t.Error("the relay did not land the agent branch on the bare repo")
			}
			if !bundleExists(t, outbox) {
				t.Error("the bundle must stay in the outbox for `spindrift recover`")
			}
			if h.Backend.PRCount() != 0 {
				t.Errorf("backend holds %d PRs, want none", h.Backend.PRCount())
			}
			if gotSt := statusesOf(rec, StatusDraftPRRetry, StatusDraftPRFinalLookup, StatusDraftPRFailed, StatusDraftPRAdopted); !slices.Equal(gotSt, tc.want) {
				t.Errorf("draft-PR statuses = %v, want %v", gotSt, tc.want)
			}
			assertOutboxParked(t, fc, "1")
		})
	}
}

// A handshake without forge.draftPRs resolves to a forge that cannot
// host-mediate, and a read-only hand-off through it settles exactly as through
// a native PR forge lacking BundleRelay: Open's wiring error blocks the
// hand-off in progress with a comment, and nothing reaches the forge.
func TestSettle_NoDraftPRCapabilityCannotHostMediateThroughConnectorForge(t *testing.T) {
	h := connectorforgetest.NewHostMediationVariant(t, connectorforgetest.MinimalPRWasm)
	caps := h.Capabilities()
	if caps.BundleRelay != nil || caps.DraftPRCreator != nil || caps.BranchPusher != nil || caps.HostCanOpenPR() {
		t.Fatalf("caps = %+v, want no BundleRelay, DraftPRCreator or BranchPusher", caps)
	}
	outbox := h.SeedBundle(handoffBranch)
	requests := h.Backend.Requests()

	got, fc, rec := readOnlySettle(t, caps, h.CodeForge(), outbox, 2)
	want, nativeFC, nativeRec := readOnlySettle(t, forge.Capabilities{}, nil, outbox, 2)

	if want.Reason() != dispatchrecord.ReasonHandoffBlocked {
		t.Fatalf("native settlement = %+v, want a blocked hand-off", want)
	}
	if got != want {
		t.Errorf("Settle = %+v, want the native %+v", got, want)
	}
	if g, w := statusesOf(rec, StatusMergeBlocked), statusesOf(nativeRec, StatusMergeBlocked); !slices.Equal(g, w) || len(g) != 1 {
		t.Errorf("merge-blocked steps = %v, want the native %v", g, w)
	}
	iss, _ := fc.Issue("1")
	nativeIss, _ := nativeFC.Issue("1")
	if !slices.Equal(iss.Labels, nativeIss.Labels) || !containsLabel(iss.Labels, "agent-in-progress") {
		t.Errorf("labels = %v, want the native %v, still in progress", iss.Labels, nativeIss.Labels)
	}
	if len(fc.CommentCalls) != len(nativeFC.CommentCalls) {
		t.Errorf("comments = %+v, want the native count: %+v", fc.CommentCalls, nativeFC.CommentCalls)
	}
	if h.Backend.Requests() != requests || h.Backend.PRCount() != 0 {
		t.Errorf("backend saw %d requests (was %d) and holds %d PRs, want it untouched", h.Backend.Requests(), requests, h.Backend.PRCount())
	}
	if exists, err := h.CodeForge().BranchExists(handoffBranch); err != nil || exists {
		t.Errorf("BranchExists = (%v, %v), want the branch unpushed", exists, err)
	}
}
