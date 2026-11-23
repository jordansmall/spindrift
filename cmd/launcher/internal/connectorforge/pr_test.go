package connectorforge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorcall"
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
	// sleeps records each pause the composed forge asked its clock for.
	sleeps []time.Duration
	// audit receives the refusals core audits.
	audit bytes.Buffer
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
		case []string:
			// Successive calls answer successive entries; the last one repeats.
			n := 0
			for _, c := range h.calls[:len(h.calls)-1] {
				if c == method {
					n++
				}
			}
			return json.Unmarshal([]byte(r[min(n, len(r)-1)]), result)
		case string:
			if result == nil {
				return nil
			}
			return json.Unmarshal([]byte(r), result)
		}
		panic("bad reply")
	}
	clock := retry.Clock{Now: time.Now, Sleep: func(d time.Duration) { h.sleeps = append(h.sleeps, d) }}
	conn := auditStub{&h.audit}
	p := &pushOnly{conn: conn, call: call, clock: clock, target: testTarget, git: h.git}
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

func TestMarkReadyMapsTransientToTheRetriedSentinel(t *testing.T) {
	h := newPRHarness(t, nil, map[string]any{"forge.setDraft": classed(connectorhost.Transient)})
	err := h.pr().MarkReady(prURL7)
	if !errors.Is(err, forge.ErrMarkReadyTransient) {
		t.Fatalf("MarkReady = %v, want ErrMarkReadyTransient", err)
	}
	if connectorhost.ErrorClass(err) != connectorhost.Transient {
		t.Errorf("class lost: %v", err)
	}
	if !strings.HasSuffix(err.Error(), forge.ErrMarkReadyTransient.Error()) {
		t.Errorf("MarkReady = %q, want the sentinel text last, as the github adapter renders it", err)
	}
	h = newPRHarness(t, nil, map[string]any{"forge.setDraft": classed(connectorhost.Transient)})
	if err := h.pr().MarkDraft(prURL7); errors.Is(err, forge.ErrMarkReadyTransient) {
		t.Errorf("MarkDraft = %v, must not carry the mark-ready sentinel", err)
	}
}

func TestMergeFailureIsClassifiedFromThePR(t *testing.T) {
	notMergeable := classed(connectorhost.NotMergeable)
	for _, tc := range []struct {
		name   string
		err    error
		pr     string
		reread bool
		want   func(error) bool
	}{
		{"conflicting", notMergeable, prJSON + `,"mergeable":"CONFLICTING"}`, true, func(e error) bool { return errors.Is(e, forge.ErrMergeConflict) }},
		{"blocked by checks", notMergeable, prJSON + `,"mergeable":"MERGEABLE"}`, true, func(e error) bool { return errors.Is(e, forge.ErrMergeBlockedByChecks) }},
		{"already merged", notMergeable, `{"number":"7","url":"` + prURL7 + `","state":"closed","merged":true}`, true, func(e error) bool { return e == nil }},
		{"closed", notMergeable, `{"number":"7","url":"` + prURL7 + `","state":"closed","merged":false,"mergeable":"MERGEABLE"}`, true, notSentinel},
		{"draft", notMergeable, prJSON + `,"draft":true,"mergeable":"CONFLICTING"}`, true, notSentinel},
		{"undetermined", notMergeable, prJSON + `,"mergeable":"UNKNOWN"}`, true, notSentinel},
		{"merge method not allowed on a mergeable PR", &connectorhost.CallError{Class: connectorhost.Permanent, Message: "Merge method not allowed"}, prJSON + `,"mergeable":"MERGEABLE"}`, false, notSentinel},
		{"permanent on a conflicting PR", classed(connectorhost.Permanent), prJSON + `,"mergeable":"CONFLICTING"}`, false, notSentinel},
		{"not found", classed(connectorhost.NotFound), prJSON + `,"mergeable":"CONFLICTING"}`, false, notSentinel},
		{"unclassified", connectorhost.ErrBudget, prJSON + `,"mergeable":"CONFLICTING"}`, false, notSentinel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPRHarness(t, nil, map[string]any{"forge.merge": tc.err, "forge.pr": tc.pr})
			if err := h.f.Merge(prURL7); !tc.want(err) {
				t.Fatalf("Merge = %v", err)
			}
			reads := 0
			for _, c := range h.calls {
				if c == "forge.pr" {
					reads++
				}
			}
			if (reads > 0) != tc.reread {
				t.Errorf("PR reads = %d, want a re-read: %t (calls %v)", reads, tc.reread, h.calls)
			}
			if !tc.reread && (len(h.calls) != 1 || len(h.sleeps) != 0) {
				t.Errorf("calls = %v, sleeps = %v, want the merge only", h.calls, h.sleeps)
			}
		})
	}
}

func notSentinel(e error) bool {
	return e != nil && !errors.Is(e, forge.ErrMergeConflict) && !errors.Is(e, forge.ErrMergeBlockedByChecks)
}

func TestMergeRereadsAConflictingPROnceAfterAPause(t *testing.T) {
	conflicting, mergeable := prJSON+`,"mergeable":"CONFLICTING"}`, prJSON+`,"mergeable":"MERGEABLE"}`
	for _, tc := range []struct {
		name       string
		reads      []string
		want       error
		wantSleeps int
		wantPRRead int
	}{
		{"recomputed after the pause", []string{conflicting, mergeable}, forge.ErrMergeBlockedByChecks, 1, 2},
		{"still conflicting", []string{conflicting, conflicting}, forge.ErrMergeConflict, 1, 2},
		{"conflicting only once", []string{conflicting, conflicting, mergeable}, forge.ErrMergeConflict, 1, 2},
		{"blocked by checks reads once", []string{mergeable, conflicting}, forge.ErrMergeBlockedByChecks, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPRHarness(t, nil, map[string]any{"forge.merge": classed(connectorhost.NotMergeable), "forge.pr": tc.reads})
			if err := h.f.Merge(prURL7); !errors.Is(err, tc.want) {
				t.Fatalf("Merge = %v, want %v", err, tc.want)
			}
			if len(h.sleeps) != tc.wantSleeps || (tc.wantSleeps > 0 && h.sleeps[0] != mergeRecheckDelay) {
				t.Errorf("sleeps = %v, want %d of %v", h.sleeps, tc.wantSleeps, mergeRecheckDelay)
			}
			reads := 0
			for _, c := range h.calls {
				if c == "forge.pr" {
					reads++
				}
			}
			if reads != tc.wantPRRead {
				t.Errorf("PR reads = %d, want %d (calls %v)", reads, tc.wantPRRead, h.calls)
			}
		})
	}
}

func TestMergeDoesNotPauseForAMergedOrDraftPR(t *testing.T) {
	for name, pr := range map[string]string{
		"merged": `{"number":"7","url":"` + prURL7 + `","state":"closed","merged":true,"mergeable":"CONFLICTING"}`,
		"draft":  prJSON + `,"draft":true,"mergeable":"CONFLICTING"}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newPRHarness(t, nil, map[string]any{"forge.merge": classed(connectorhost.NotMergeable), "forge.pr": pr})
			_ = h.f.Merge(prURL7)
			if len(h.sleeps) != 0 {
				t.Errorf("sleeps = %v, want none", h.sleeps)
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
		"forge.merge": 1, "forge.openPR": 1, "forge.comment": 1, "forge.pr": connectorcall.Attempts, "forge.setDraft": connectorcall.Attempts,
	} {
		calls := 0
		call := connectorcall.Classify(func(context.Context, string, any, any) error {
			calls++
			return classed(connectorhost.Transient)
		}, clock, CallPolicy())
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
	if h.caps.PRForge == nil || h.caps.BranchDeleter == nil || h.caps.MergeCommitReader == nil {
		t.Errorf("base capabilities missing: %+v", h.caps)
	}
	if h.caps.CIRunReporter != nil || h.caps.BranchProtectionForge != nil || h.caps.DraftPRCreator != nil || h.caps.BranchPusher != nil {
		t.Errorf("undeclared surfaces present: %+v", h.caps)
	}

	if h := newPRHarness(t, []string{capCIRun}, nil); h.caps.CIRunReporter == nil || h.caps.BranchProtectionForge != nil {
		t.Errorf("forge.ciRun alone resolved %+v", h.caps)
	}
	if h := newPRHarness(t, []string{capBranchProtection}, nil); h.caps.CIRunReporter != nil || h.caps.BranchProtectionForge == nil {
		t.Errorf("forge.branchProtection alone resolved %+v", h.caps)
	}
	if h := newPRHarness(t, []string{capDraftPRs}, nil); h.caps.DraftPRCreator == nil || h.caps.BranchPusher == nil || !h.caps.HostCanOpenPR() || h.caps.CIRunReporter != nil || h.caps.BranchProtectionForge != nil || h.caps.PRForge == nil {
		t.Errorf("forge.draftPRs alone resolved %+v", h.caps)
	}
	if h := newPRHarness(t, []string{capCIRun, capBranchProtection, capDraftPRs}, nil); h.caps.CIRunReporter == nil || h.caps.BranchProtectionForge == nil || !h.caps.HostCanOpenPR() || h.caps.PRForge == nil || h.caps.MergeCommitReader == nil || h.caps.BranchDeleter == nil {
		t.Errorf("every optional surface stacked resolved %+v", h.caps)
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
	h := newPRHarness(t, nil, map[string]any{"forge.merge": classed(connectorhost.NotMergeable), "forge.pr": classed(connectorhost.Permanent)})
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

const sha40 = "0123456789abcdef0123456789abcdef01234567"

func TestMergeCommitMustBeFullHex(t *testing.T) {
	for name, tc := range map[string]struct {
		commit  string
		wantErr bool
	}{
		"empty (not merged)": {"", false},
		"sha1":               {sha40, false},
		"sha256":             {strings.Repeat("a1", 32), false},
		"short":              {"def", true},
		"not hex":            {"not-a-sha", true},
		"39 hex":             {sha40[:39], true},
		"non-hex char":       {sha40[:39] + "g", true},
		"uppercase":          {strings.ToUpper(sha40), true},
		"41 hex":             {sha40 + "a", true},
	} {
		t.Run(name, func(t *testing.T) {
			h := newPRHarness(t, nil, map[string]any{"forge.pr": prJSON + `,"mergeCommit":"` + tc.commit + `"}`})
			got, err := h.caps.MergeCommitReader.MergeCommit(prURL7)
			if tc.wantErr {
				if got != "" || !errors.Is(err, ErrAnswer) {
					t.Errorf("MergeCommit = (%q, %v), want ErrAnswer", got, err)
				}
				return
			}
			if got != tc.commit || err != nil {
				t.Errorf("MergeCommit = (%q, %v), want %q", got, err, tc.commit)
			}
		})
	}
}

func TestHeadCommitSHAAndMergeCommit(t *testing.T) {
	h := newPRHarness(t, nil, map[string]any{"forge.pr": prJSON + `,"headSha":"abc","mergeCommit":"` + sha40 + `"}`})
	if sha, err := h.pr().HeadCommitSHA(prURL7); sha != "abc" || err != nil {
		t.Errorf("HeadCommitSHA = (%q, %v)", sha, err)
	}
	if c, err := h.caps.MergeCommitReader.MergeCommit(prURL7); c != sha40 || err != nil {
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

func TestHeadOfDecodesCrossRepo(t *testing.T) {
	for name, tc := range map[string]struct {
		extra string
		want  bool
	}{
		"cross-repo": {`,"crossRepo":true`, true},
		"absent":     {``, false},
	} {
		h := newPRHarness(t, nil, map[string]any{"forge.pr": prJSON + `,"head":"agent/7"` + tc.extra + `}`})
		head, cross, err := HeadOf(h.f, prURL7)
		if head != "agent/7" || cross != tc.want || err != nil {
			t.Errorf("%s: HeadOf = (%q, %v, %v), want (agent/7, %v, nil)", name, head, cross, err, tc.want)
		}
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

func TestListPRFilesRefusesAnUnreadableList(t *testing.T) {
	for name, reply := range map[string]string{
		"missing field":   `{}`,
		"null":            `{"files":null}`,
		"no entries":      `{"files":[]}`,
		"empty entry":     `{"files":["a",""]}`,
		"absolute":        `{"files":["/etc/passwd"]}`,
		"leading dotdot":  `{"files":["../x"]}`,
		"inner dotdot":    `{"files":["a/../b"]}`,
		"trailing dotdot": `{"files":["a/.."]}`,
		"newline":         `{"files":["a\nb"]}`,
		"NUL":             `{"files":["a\u0000b"]}`,
		"DEL":             `{"files":["a\u007fb"]}`,
		"dot segment":     `{"files":["./.github/workflows/ci.yml"]}`,
		"empty segment":   `{"files":["a//b"]}`,
		"trailing slash":  `{"files":["a/"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newPRHarness(t, nil, map[string]any{"forge.prFiles": reply})
			files, err := h.pr().ListPRFiles(prURL7)
			if !errors.Is(err, ErrAnswer) || files != nil {
				t.Fatalf("ListPRFiles = (%v, %v), want (nil, ErrAnswer)", files, err)
			}
			if !strings.Contains(err.Error(), "file list") {
				t.Errorf("error %q does not name the file list", err)
			}
		})
	}
}

func TestListPRFilesNamesAMissingField(t *testing.T) {
	h := newPRHarness(t, nil, map[string]any{"forge.prFiles": `{}`})
	_, err := h.pr().ListPRFiles(prURL7)
	if !errors.Is(err, ErrAnswer) || !strings.Contains(err.Error(), "PR 7 file list is missing") {
		t.Fatalf("ListPRFiles({}) = %v, want ErrAnswer naming the missing list", err)
	}
}

func TestListPRFilesKeepsCleanPaths(t *testing.T) {
	h := newPRHarness(t, nil, map[string]any{"forge.prFiles": `{"files":[".github/workflows/ci.yml","a..b/c","..hidden","dir/file with space.txt"]}`})
	files, err := h.pr().ListPRFiles(prURL7)
	if want := ".github/workflows/ci.yml,a..b/c,..hidden,dir/file with space.txt"; err != nil || strings.Join(files, ",") != want {
		t.Errorf("ListPRFiles = (%v, %v), want %s", files, err, want)
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

func TestRebasePRHeadPinsTheHeadBranch(t *testing.T) {
	const want = "agent/issue-7"
	reply := func(head string) map[string]any {
		return map[string]any{"forge.pr": prJSON + `,"head":"` + head + `"}`}
	}
	t.Run("rebases the expected head", func(t *testing.T) {
		h := newPRHarness(t, nil, reply(want))
		if err := h.caps.PRHeadRebaser.RebasePRHead(prURL7, want); err != nil {
			t.Fatal(err)
		}
		if len(h.git.rebased) != 1 || h.git.rebased[0] != want {
			t.Errorf("rebased = %v, want [%s]", h.git.rebased, want)
		}
	})
	for _, tc := range []struct{ name, head, want string }{
		{"refuses an unrelated head", "release", want},
		{"refuses a head equal to the base", "main", want},
		{"refuses another agent branch", "agent/issue-999", want},
		{"refuses an empty expected head", want, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPRHarness(t, nil, reply(tc.head))
			err := h.caps.PRHeadRebaser.RebasePRHead(prURL7, tc.want)
			if !errors.Is(err, ErrAnswer) {
				t.Fatalf("RebasePRHead = %v, want ErrAnswer", err)
			}
			if len(h.git.rebased) != 0 {
				t.Errorf("rebased = %v, want none", h.git.rebased)
			}
		})
	}
	t.Run("audits the refusal", func(t *testing.T) {
		h := newPRHarness(t, nil, reply("release"))
		_ = h.caps.PRHeadRebaser.RebasePRHead(prURL7, want)
		if got := h.audit.String(); !strings.Contains(got, "refused") || !strings.Contains(got, "forge.pr") {
			t.Errorf("audit = %q, want a refused forge.pr entry", got)
		}
	})
}

func TestCreateDraftPR(t *testing.T) {
	created := prJSON + `}`
	h := newPRHarness(t, []string{capDraftPRs}, map[string]any{"forge.openPR": created})
	if url, ok, err := h.caps.DraftPRCreator.CreateDraftPR("t", "b", "main", "agent/issue-1"); url != prURL7 || !ok || err != nil {
		t.Fatalf("CreateDraftPR = (%q, %v, %v)", url, ok, err)
	}

	t.Run("adopts an existing open PR", func(t *testing.T) {
		h := newPRHarness(t, []string{capDraftPRs}, map[string]any{
			"forge.openPR":       classed(connectorhost.Permanent),
			"forge.prsForBranch": `{"prs":[` + created + `]}`})
		if url, ok, err := h.caps.DraftPRCreator.CreateDraftPR("t", "b", "main", "agent/issue-1"); url != prURL7 || ok || err != nil {
			t.Fatalf("CreateDraftPR = (%q, %v, %v), want adopted", url, ok, err)
		}
	})
	t.Run("returns the create error when nothing is open", func(t *testing.T) {
		cause := classed(connectorhost.Permanent)
		h := newPRHarness(t, []string{capDraftPRs}, map[string]any{"forge.openPR": cause, "forge.prsForBranch": `{"prs":[]}`})
		if _, _, err := h.caps.DraftPRCreator.CreateDraftPR("t", "b", "main", "agent/issue-1"); !errors.Is(err, cause) {
			t.Fatalf("CreateDraftPR = %v, want the create error", err)
		}
	})
	t.Run("refuses a foreign PR", func(t *testing.T) {
		h := newPRHarness(t, []string{capDraftPRs}, map[string]any{"forge.openPR": `{"number":"7","url":"https://evil.example/o/r/pull/7","state":"open","merged":false}`})
		if _, _, err := h.caps.DraftPRCreator.CreateDraftPR("t", "b", "main", "h"); !errors.Is(err, ErrAnswer) {
			t.Fatalf("CreateDraftPR = %v, want ErrAnswer", err)
		}
	})
}

func TestRebaseRebasesTheHeadBranch(t *testing.T) {
	h := newPRHarness(t, nil, map[string]any{"forge.pr": prJSON + `,"head":"agent/issue-1"}`})
	if err := h.caps.PRHeadRebaser.RebasePRHead(prURL7, "agent/issue-1"); err != nil {
		t.Fatal(err)
	}
	if len(h.git.rebased) != 1 || h.git.rebased[0] != "agent/issue-1" {
		t.Errorf("rebased = %v, want [agent/issue-1]", h.git.rebased)
	}
	h = newPRHarness(t, nil, map[string]any{"forge.pr": prJSON + `}`})
	if err := h.caps.PRHeadRebaser.RebasePRHead(prURL7, "agent/issue-1"); !errors.Is(err, ErrAnswer) || len(h.git.rebased) != 0 {
		t.Errorf("RebasePRHead with no head = %v, rebased %v; want ErrAnswer and no rebase", err, h.git.rebased)
	}
}

// A plain Rebase has no expected head to pin, so it refuses before reading the
// PR or running any git (issue #6961).
func TestPlainRebaseIsRefused(t *testing.T) {
	for _, head := range []string{"agent/issue-7", "main", "release"} {
		h := newPRHarness(t, nil, map[string]any{"forge.pr": prJSON + `,"head":"` + head + `"}`})
		if err := h.f.Rebase(prURL7); !errors.Is(err, ErrAnswer) {
			t.Errorf("Rebase with head %q = %v, want ErrAnswer", head, err)
		}
		if len(h.git.rebased) != 0 {
			t.Errorf("Rebase with head %q rebased %v, want none", head, h.git.rebased)
		}
		if len(h.calls) != 0 {
			t.Errorf("Rebase with head %q called %v, want no Connector call", head, h.calls)
		}
	}
}

func TestRebaseWithoutGitFailsNamed(t *testing.T) {
	h := newPRHarness(t, nil, map[string]any{"forge.pr": prJSON + `,"head":"agent/issue-1"}`})
	h.f.(*prForge).push.git = nil
	if err := h.caps.PRHeadRebaser.RebasePRHead(prURL7, "agent/issue-1"); !errors.Is(err, ErrNoGit) {
		t.Fatalf("Rebase with no git = %v, want ErrNoGit", err)
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

// auditStub records AuditRefusal entries in place of a network Connector's log.
type auditStub struct{ audit *bytes.Buffer }

func (auditStub) DeclaredHosts() []string { return nil }

func (s auditStub) AuditRefusal(method string, refusal error) error {
	s.audit.WriteString("refused " + method + ": " + refusal.Error() + "\n")
	return refusal
}

func classed(c connectorhost.Class) error {
	return &connectorhost.CallError{Class: c, Message: "x"}
}
