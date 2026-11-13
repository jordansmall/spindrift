package connectorforge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
)

type recordingGit struct {
	forge.CodeForge
	rebased []string
}

func (g *recordingGit) Rebase(ref string) error { g.rebased = append(g.rebased, ref); return nil }

const runURL = "https://github.com/o/r/actions/runs/1"

const prJSON = `{"number":"7","url":"` + prURL7 + `","state":"open","merged":false`

// prHarness composes a PR forge over a stub guest whose replies map a method to
// either a JSON string or an error.
type prHarness struct {
	f     forge.CodeForge
	caps  forge.Capabilities
	calls []string
	git   *recordingGit
}

func newPRHarness(t *testing.T, caps []string, replies map[string]any) *prHarness {
	t.Helper()
	h := &prHarness{git: &recordingGit{}}
	call := func(_ context.Context, method string, _, result any) error {
		h.calls = append(h.calls, method)
		switch r := replies[method].(type) {
		case nil:
			return errors.New("guest: no such method " + method)
		case error:
			return r
		case string:
			if result == nil {
				return nil
			}
			return json.Unmarshal([]byte(r), result)
		}
		panic("bad reply")
	}
	p := &pushOnly{call: call, target: testTarget, git: h.git}
	h.f = composePR(p, caps, testWebHost)
	h.caps = forge.ResolveCodeForge(h.f)
	return h
}

func (h *prHarness) pr() forge.PRForge { return h.caps.PRForge }

func TestMergeMapsTransientToTheRetriedSentinel(t *testing.T) {
	h := newPRHarness(t, nil, map[string]any{"forge.merge": classed(connectorhost.Transient)})
	err := h.pr().(forge.CodeForge).Merge(prURL7)
	if !errors.Is(err, forge.ErrMergeTransient) {
		t.Fatalf("Merge = %v, want ErrMergeTransient", err)
	}
	if connectorhost.ErrorClass(err) != connectorhost.Transient {
		t.Errorf("class lost: %v", err)
	}
	if len(h.calls) != 1 {
		t.Errorf("calls = %v, want the merge only (no PR re-read)", h.calls)
	}
}

func TestMergeFailureIsClassifiedFromThePR(t *testing.T) {
	for _, tc := range []struct {
		name string
		pr   string
		want func(error) bool
	}{
		{"conflicting", prJSON + `,"mergeable":"CONFLICTING"}`, func(e error) bool { return errors.Is(e, forge.ErrMergeConflict) }},
		{"blocked by checks", prJSON + `,"mergeable":"MERGEABLE"}`, func(e error) bool { return errors.Is(e, forge.ErrMergeBlockedByChecks) }},
		{"already merged", `{"number":"7","url":"` + prURL7 + `","state":"closed","merged":true}`, func(e error) bool { return e == nil }},
		{"closed", `{"number":"7","url":"` + prURL7 + `","state":"closed","merged":false,"mergeable":"MERGEABLE"}`, func(e error) bool {
			return e != nil && !errors.Is(e, forge.ErrMergeConflict) && !errors.Is(e, forge.ErrMergeBlockedByChecks)
		}},
		{"draft", prJSON + `,"draft":true,"mergeable":"CONFLICTING"}`, func(e error) bool {
			return e != nil && !errors.Is(e, forge.ErrMergeConflict) && !errors.Is(e, forge.ErrMergeBlockedByChecks)
		}},
		{"undetermined", prJSON + `,"mergeable":"UNKNOWN"}`, func(e error) bool {
			return e != nil && !errors.Is(e, forge.ErrMergeConflict) && !errors.Is(e, forge.ErrMergeBlockedByChecks)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPRHarness(t, nil, map[string]any{"forge.merge": classed(connectorhost.Permanent), "forge.pr": tc.pr})
			if err := h.f.Merge(prURL7); !tc.want(err) {
				t.Fatalf("Merge = %v", err)
			}
		})
	}
}

func TestMergeLeavesRateLimitAndAuthUnclassified(t *testing.T) {
	rl := &forge.RateLimitError{}
	h := newPRHarness(t, nil, map[string]any{"forge.merge": rl})
	if err := h.f.Merge(prURL7); !errors.Is(err, forge.ErrRateLimit) {
		t.Fatalf("Merge = %v, want ErrRateLimit", err)
	}
	h = newPRHarness(t, nil, map[string]any{"forge.merge": fmt.Errorf("%w: x", forge.ErrAuthFailure)})
	if err := h.f.Merge(prURL7); !errors.Is(err, forge.ErrAuthFailure) {
		t.Fatalf("Merge = %v, want ErrAuthFailure", err)
	}
	if len(h.calls) != 1 {
		t.Errorf("calls = %v, want no PR re-read", h.calls)
	}
}

func TestMergeReturnsARefusedCallAsIsWithoutRereadingThePR(t *testing.T) {
	refused := fmt.Errorf("%w: %w", connectorhost.ErrCall, connectorhost.ErrRouteRefused)
	h := newPRHarness(t, nil, map[string]any{"forge.merge": refused})
	err := h.f.Merge(prURL7)
	if err != refused {
		t.Fatalf("Merge = %v, want the refusal returned as is", err)
	}
	if len(h.calls) != 1 {
		t.Errorf("calls = %v, want the merge only (a refused Connector is closed)", h.calls)
	}
}

func TestClassifyDoesNotRetryNonIdempotentWrites(t *testing.T) {
	clock := retry.Clock{Now: time.Now, Sleep: func(time.Duration) {}}
	for method, wantCalls := range map[string]int{
		"forge.merge": 1, "forge.openPR": 1, "forge.comment": 1, "forge.pr": callAttempts, "forge.setDraft": callAttempts,
	} {
		calls := 0
		call := classify(func(context.Context, string, any, any) error {
			calls++
			return classed(connectorhost.Transient)
		}, clock)
		err := call(context.Background(), method, nil, nil)
		if connectorhost.ErrorClass(err) != connectorhost.Transient {
			t.Errorf("%s: err = %v, want the transient error", method, err)
		}
		if calls != wantCalls {
			t.Errorf("%s: %d calls, want %d", method, calls, wantCalls)
		}
	}
}

func TestCanAutoMergeNeedsTheCapability(t *testing.T) {
	h := newPRHarness(t, nil, nil)
	if ok, err := h.pr().CanAutoMerge(); ok || err != nil {
		t.Fatalf("CanAutoMerge = (%v, %v), want (false, nil)", ok, err)
	}
	if err := h.pr().EnqueueAutoMerge(prURL7); err == nil || !strings.Contains(err.Error(), capAutoMerge) {
		t.Fatalf("EnqueueAutoMerge = %v, want an error naming %s", err, capAutoMerge)
	}
	if len(h.calls) != 0 {
		t.Errorf("calls = %v, want none", h.calls)
	}

	h = newPRHarness(t, []string{capAutoMerge}, map[string]any{
		"forge.autoMergeAllowed": `{"allowed":true}`, "forge.enqueueAutoMerge": `{}`})
	if ok, err := h.pr().CanAutoMerge(); !ok || err != nil {
		t.Fatalf("CanAutoMerge = (%v, %v), want (true, nil)", ok, err)
	}
	if err := h.pr().EnqueueAutoMerge(prURL7); err != nil {
		t.Fatalf("EnqueueAutoMerge: %v", err)
	}
}

func TestOptionalSurfacesFollowTheCapabilities(t *testing.T) {
	h := newPRHarness(t, nil, nil)
	if h.caps.PRForge == nil || h.caps.DraftPRCreator == nil || h.caps.BranchDeleter == nil || h.caps.MergeCommitReader == nil {
		t.Errorf("base capabilities missing: %+v", h.caps)
	}
	if h.caps.CIRunReporter != nil || h.caps.BranchProtectionForge != nil {
		t.Errorf("undeclared surfaces present: %+v", h.caps)
	}

	if h := newPRHarness(t, []string{capCIRun}, nil); h.caps.CIRunReporter == nil || h.caps.BranchProtectionForge != nil {
		t.Errorf("forge.ciRun alone resolved %+v", h.caps)
	}
	if h := newPRHarness(t, []string{capBranchProtection}, nil); h.caps.CIRunReporter != nil || h.caps.BranchProtectionForge == nil {
		t.Errorf("forge.branchProtection alone resolved %+v", h.caps)
	}

	h = newPRHarness(t, []string{capCIRun, capBranchProtection}, map[string]any{
		"forge.checks": `{"state":"PENDING","checks":[]}`, "forge.ciRun": `{"url":"` + runURL + `"}`,
		"forge.branchProtected": `{"protected":true}`})
	state, url, err := h.caps.CIRunReporter.CheckRun(prURL7)
	if err != nil || state != forge.StatePending || url != runURL {
		t.Fatalf("CheckRun = (%q, %q, %v)", state, url, err)
	}
	if ok, err := h.caps.BranchProtectionForge.BranchProtected("main"); !ok || err != nil {
		t.Fatalf("BranchProtected = (%v, %v)", ok, err)
	}
}

func TestCheckRunDropsAForeignRunLink(t *testing.T) {
	for _, tc := range []struct{ name, link, want string }{
		{"own repo run", runURL, runURL},
		{"own repo, mixed case", "https://GitHub.com/O/R/actions/runs/1", "https://GitHub.com/O/R/actions/runs/1"},
		{"empty", "", ""},
		{"foreign host", "https://attacker.example/o/r/actions/runs/1", ""},
		{"http scheme", "http://github.com/o/r/actions/runs/1", ""},
		{"other repo", "https://github.com/x/y/actions/runs/1", ""},
		{"repo-prefix sibling", "https://github.com/o/r2/actions/runs/1", ""},
		{"userinfo", "https://u@github.com/o/r/actions/runs/1", ""},
		{"dot segments", "https://github.com/o/r/../../x/y/actions/runs/1", ""},
		{"opaque", "https:github.com/o/r/actions/runs/1", ""},
		{"unparseable", "https://github.com/o/r/%zz", ""},
		{"backslash escape", `https://github.com/o/r/..\..\x/y/actions/runs/1`, ""},
		{"query", "https://github.com/o/r/actions/runs/1?next=https://evil.example", ""},
		{"empty query", "https://github.com/o/r/actions/runs/1?", ""},
		{"fragment", "https://github.com/o/r/actions/runs/1#x", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply, err := json.Marshal(map[string]string{"url": tc.link})
			if err != nil {
				t.Fatal(err)
			}
			h := newPRHarness(t, []string{capCIRun}, map[string]any{
				"forge.checks": `{"state":"PENDING","checks":[]}`, "forge.ciRun": string(reply)})
			state, got, err := h.caps.CIRunReporter.CheckRun(prURL7)
			if err != nil || state != forge.StatePending || got != tc.want {
				t.Fatalf("CheckRun = (%q, %q, %v), want (PENDING, %q, nil)", state, got, err, tc.want)
			}
		})
	}
}

func TestMergeFailureKeepsTheCauseWhenThePRLookupFails(t *testing.T) {
	h := newPRHarness(t, nil, map[string]any{"forge.merge": classed(connectorhost.Permanent), "forge.pr": classed(connectorhost.Permanent)})
	err := h.f.Merge(prURL7)
	if err == nil || errors.Is(err, forge.ErrMergeConflict) || errors.Is(err, forge.ErrMergeBlockedByChecks) {
		t.Fatalf("Merge = %v, want a non-sentinel error", err)
	}
	if !strings.Contains(err.Error(), "PR lookup failed") {
		t.Errorf("Merge = %v, want it to mention the lookup failure", err)
	}
}

func TestMergeableFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		mergeable string
		want      forge.MergeableState
		wantErr   bool
	}{
		{"MERGEABLE", forge.MergeableMergeable, false},
		{"CONFLICTING", forge.MergeableConflicting, false},
		{"UNKNOWN", forge.MergeableUnknown, false},
		{"", "", true},
		{"CLEAN", "", true},
	} {
		h := newPRHarness(t, nil, map[string]any{"forge.pr": prJSON + `,"mergeable":"` + tc.mergeable + `"}`})
		got, err := h.pr().Mergeable(prURL7)
		if got != tc.want || (err != nil) != tc.wantErr || (err != nil && !errors.Is(err, ErrAnswer)) {
			t.Errorf("Mergeable(%q) = (%q, %v)", tc.mergeable, got, err)
		}
	}
}

func TestHeadCommitSHAAndMergeCommit(t *testing.T) {
	h := newPRHarness(t, nil, map[string]any{"forge.pr": prJSON + `,"headSha":"abc","mergeCommit":"def"}`})
	if sha, err := h.pr().HeadCommitSHA(prURL7); sha != "abc" || err != nil {
		t.Errorf("HeadCommitSHA = (%q, %v)", sha, err)
	}
	if c, err := h.caps.MergeCommitReader.MergeCommit(prURL7); c != "def" || err != nil {
		t.Errorf("MergeCommit = (%q, %v)", c, err)
	}
	h = newPRHarness(t, nil, map[string]any{"forge.pr": prJSON + `}`})
	if _, err := h.pr().HeadCommitSHA(prURL7); !errors.Is(err, ErrAnswer) {
		t.Errorf("HeadCommitSHA with no sha = %v, want ErrAnswer", err)
	}
	if c, err := h.caps.MergeCommitReader.MergeCommit(prURL7); c != "" || err != nil {
		t.Errorf("MergeCommit = (%q, %v), want empty", c, err)
	}
}

func TestNeedsUpdateAndListPRFiles(t *testing.T) {
	for reply, want := range map[string]bool{`{"behindBy":0}`: false, `{"behindBy":3}`: true} {
		h := newPRHarness(t, nil, map[string]any{"forge.behind": reply})
		if got, err := h.pr().NeedsUpdate(prURL7); got != want || err != nil {
			t.Errorf("NeedsUpdate(%s) = (%v, %v)", reply, got, err)
		}
	}
	h := newPRHarness(t, nil, map[string]any{"forge.behind": `{"behindBy":-1}`, "forge.prFiles": `{"files":["a","b"]}`})
	if _, err := h.pr().NeedsUpdate(prURL7); !errors.Is(err, ErrAnswer) {
		t.Errorf("negative behindBy = %v, want ErrAnswer", err)
	}
	if files, err := h.pr().ListPRFiles(prURL7); len(files) != 2 || err != nil {
		t.Errorf("ListPRFiles = (%v, %v)", files, err)
	}
}

func TestMarkDraftCommentAndURLPinning(t *testing.T) {
	h := newPRHarness(t, nil, map[string]any{"forge.setDraft": `{}`, "forge.comment": `{}`})
	for _, err := range []error{h.pr().MarkReady(prURL7), h.pr().MarkDraft(prURL7), h.pr().CommentPR(prURL7, "hi")} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if want := "forge.setDraft forge.setDraft forge.comment"; strings.Join(h.calls, " ") != want {
		t.Errorf("calls = %v, want %s", h.calls, want)
	}
	if err := h.pr().MarkReady("https://evil.example/o/r/pull/7"); err == nil || len(h.calls) != 3 {
		t.Errorf("MarkReady on a foreign URL = %v, calls %v; want a refusal before any call", err, h.calls)
	}
}

func TestCreateDraftPR(t *testing.T) {
	created := prJSON + `}`
	h := newPRHarness(t, nil, map[string]any{"forge.openPR": created})
	if url, ok, err := h.caps.DraftPRCreator.CreateDraftPR("t", "b", "main", "agent/issue-1"); url != prURL7 || !ok || err != nil {
		t.Fatalf("CreateDraftPR = (%q, %v, %v)", url, ok, err)
	}

	t.Run("adopts an existing open PR", func(t *testing.T) {
		h := newPRHarness(t, nil, map[string]any{
			"forge.openPR":       classed(connectorhost.Permanent),
			"forge.prsForBranch": `{"prs":[` + created + `]}`})
		if url, ok, err := h.caps.DraftPRCreator.CreateDraftPR("t", "b", "main", "agent/issue-1"); url != prURL7 || ok || err != nil {
			t.Fatalf("CreateDraftPR = (%q, %v, %v), want adopted", url, ok, err)
		}
	})
	t.Run("returns the create error when nothing is open", func(t *testing.T) {
		cause := classed(connectorhost.Permanent)
		h := newPRHarness(t, nil, map[string]any{"forge.openPR": cause, "forge.prsForBranch": `{"prs":[]}`})
		if _, _, err := h.caps.DraftPRCreator.CreateDraftPR("t", "b", "main", "agent/issue-1"); !errors.Is(err, cause) {
			t.Fatalf("CreateDraftPR = %v, want the create error", err)
		}
	})
	t.Run("refuses a foreign PR", func(t *testing.T) {
		h := newPRHarness(t, nil, map[string]any{"forge.openPR": `{"number":"7","url":"https://evil.example/o/r/pull/7","state":"open","merged":false}`})
		if _, _, err := h.caps.DraftPRCreator.CreateDraftPR("t", "b", "main", "h"); !errors.Is(err, ErrAnswer) {
			t.Fatalf("CreateDraftPR = %v, want ErrAnswer", err)
		}
	})
}

func TestRebaseRebasesTheHeadBranch(t *testing.T) {
	h := newPRHarness(t, nil, map[string]any{"forge.pr": prJSON + `,"head":"agent/issue-1"}`})
	if err := h.f.Rebase(prURL7); err != nil {
		t.Fatal(err)
	}
	if len(h.git.rebased) != 1 || h.git.rebased[0] != "agent/issue-1" {
		t.Errorf("rebased = %v, want [agent/issue-1]", h.git.rebased)
	}
	h = newPRHarness(t, nil, map[string]any{"forge.pr": prJSON + `}`})
	if err := h.f.Rebase(prURL7); !errors.Is(err, ErrAnswer) || len(h.git.rebased) != 0 {
		t.Errorf("Rebase with no head = %v, rebased %v; want ErrAnswer and no rebase", err, h.git.rebased)
	}
}

func TestProbe(t *testing.T) {
	h := newPRHarness(t, []string{capProbe}, map[string]any{"forge.probe": `{"repo":"o/r"}`})
	if repo, err := h.f.Probe(); repo != "o/r" || err != nil {
		t.Fatalf("Probe = (%q, %v)", repo, err)
	}
	h = newPRHarness(t, []string{capProbe}, map[string]any{"forge.probe": `{"repo":""}`})
	if _, err := h.f.Probe(); !errors.Is(err, ErrAnswer) {
		t.Fatalf("Probe with no repo = %v, want ErrAnswer", err)
	}
}
