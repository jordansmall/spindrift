package settle

import (
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorforge/connectorforgetest"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
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
