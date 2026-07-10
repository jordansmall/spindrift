package forge_test

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestFake_CheckStateScript verifies that CheckState pops scripted states in
// order, returning NONE once the queue is exhausted.
func TestFake_CheckStateScript(t *testing.T) {
	f := forge.NewFake()
	const url = "https://github.com/owner/repo/pull/1"
	f.SetCheckStates(url, []forge.RollupState{forge.StatePending, forge.StateSuccess})

	s1, err := f.CheckState(url)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s1 != forge.StatePending {
		t.Fatalf("poll 1: want PENDING, got %q", s1)
	}

	s2, err := f.CheckState(url)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s2 != forge.StateSuccess {
		t.Fatalf("poll 2: want SUCCESS, got %q", s2)
	}

	// Queue exhausted — expect NONE.
	s3, err := f.CheckState(url)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s3 != forge.StateNone {
		t.Fatalf("poll 3 (exhausted): want NONE, got %q", s3)
	}
}

// TestFake_TransitionState verifies that TransitionState records calls and
// swaps the from-state label for the to-state label.
func TestFake_TransitionState(t *testing.T) {
	f := forge.NewFake(testLabels)
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})

	if err := f.TransitionState("42", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("TransitionState: %v", err)
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(iss.Labels) != 1 || iss.Labels[0] != "agent-in-progress" {
		t.Fatalf("want [agent-in-progress], got %v", iss.Labels)
	}
	if len(f.TransitionStateCalls) != 1 {
		t.Fatalf("want 1 TransitionStateCall, got %d", len(f.TransitionStateCalls))
	}
	got := f.TransitionStateCalls[0]
	if got.Num != "42" || got.From != forge.Dispatchable || got.To != forge.InProgress {
		t.Errorf("unexpected call: %+v", got)
	}
}

// TestFake_Comment verifies that Comment records calls in order.
func TestFake_Comment(t *testing.T) {
	f := forge.NewFake()

	if err := f.Comment("42", "first comment"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if err := f.Comment("42", "second comment"); err != nil {
		t.Fatalf("Comment: %v", err)
	}

	if len(f.CommentCalls) != 2 {
		t.Fatalf("want 2 CommentCalls, got %d", len(f.CommentCalls))
	}
	if f.CommentCalls[0].Num != "42" || f.CommentCalls[0].Body != "first comment" {
		t.Errorf("CommentCalls[0]: got %+v", f.CommentCalls[0])
	}
	if f.CommentCalls[1].Body != "second comment" {
		t.Errorf("CommentCalls[1]: got %+v", f.CommentCalls[1])
	}
}

// TestFake_Probe verifies the Probe scripting fields.
func TestFake_Probe(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		f := forge.NewFake()
		f.ProbeRepo = "owner/repo"

		repo, err := f.Probe()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if repo != "owner/repo" {
			t.Fatalf("want %q, got %q", "owner/repo", repo)
		}
	})

	t.Run("auth error", func(t *testing.T) {
		f := forge.NewFake()
		f.ProbeErr = forge.ErrAuthFailure

		_, err := f.Probe()
		if !errors.Is(err, forge.ErrAuthFailure) {
			t.Fatalf("want ErrAuthFailure, got %v", err)
		}
	})

	t.Run("repo not found", func(t *testing.T) {
		f := forge.NewFake()
		f.ProbeErr = forge.ErrRepoNotFound

		_, err := f.Probe()
		if !errors.Is(err, forge.ErrRepoNotFound) {
			t.Fatalf("want ErrRepoNotFound, got %v", err)
		}
	})
}

// TestFake_ListLabels verifies the ListLabels scripting fields.
func TestFake_ListLabels(t *testing.T) {
	t.Run("returns scripted labels", func(t *testing.T) {
		f := forge.NewFake()
		f.Labels = []string{"ready-for-agent", "agent-in-progress"}

		labels, err := f.ListLabels()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(labels) != 2 || labels[0] != "ready-for-agent" || labels[1] != "agent-in-progress" {
			t.Fatalf("want [ready-for-agent agent-in-progress], got %v", labels)
		}
	})

	t.Run("returns error", func(t *testing.T) {
		f := forge.NewFake()
		f.ListLabelsErr = errors.New("api error")

		_, err := f.ListLabels()
		if err == nil || err.Error() != "api error" {
			t.Fatalf("want api error, got %v", err)
		}
	})
}

// TestFake_CreateLabel verifies the CreateLabel scripting fields.
func TestFake_CreateLabel(t *testing.T) {
	t.Run("records call", func(t *testing.T) {
		f := forge.NewFake()

		if err := f.CreateLabel("my-label", "a desc", "0075ca"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(f.CreateLabelCalls) != 1 {
			t.Fatalf("want 1 CreateLabelCall, got %d", len(f.CreateLabelCalls))
		}
		got := f.CreateLabelCalls[0]
		if got.Name != "my-label" || got.Description != "a desc" || got.Color != "0075ca" {
			t.Errorf("unexpected call args: %+v", got)
		}
	})

	t.Run("returns scripted error", func(t *testing.T) {
		f := forge.NewFake()
		f.CreateLabelErr = errors.New("api error")

		err := f.CreateLabel("x", "y", "z")
		if err == nil || err.Error() != "api error" {
			t.Fatalf("want api error, got %v", err)
		}
	})
}

// TestFake_OpenPRForBranch verifies the branch→PR lookup.
func TestFake_OpenPRForBranch(t *testing.T) {
	f := forge.NewFake()
	f.SetPR("agent/issue-7", forge.PR{URL: "https://github.com/o/r/pull/99", IsDraft: false})

	pr, ok, err := f.OpenPRForBranch("agent/issue-7")
	if err != nil || !ok {
		t.Fatalf("want (pr,true,nil); got ok=%v err=%v", ok, err)
	}
	if pr.URL != "https://github.com/o/r/pull/99" {
		t.Fatalf("wrong URL: %q", pr.URL)
	}

	_, ok2, err2 := f.OpenPRForBranch("no-such-branch")
	if err2 != nil || ok2 {
		t.Fatalf("want (_, false, nil) for missing branch; got ok=%v err=%v", ok2, err2)
	}
}

// TestFake_FailureDetail verifies that FailureDetail returns the scripted
// detail for a PR URL, and "" (no error) for a URL with nothing scripted —
// the fetch is best-effort, so an unscripted PR must not look like a hard
// failure.
func TestFake_FailureDetail(t *testing.T) {
	f := forge.NewFake()
	const url = "https://github.com/owner/repo/pull/7"
	f.SetFailureDetail(url, "go test ./...: FAIL TestFoo\nwant 1, got 2")

	detail, err := f.FailureDetail(url)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail != "go test ./...: FAIL TestFoo\nwant 1, got 2" {
		t.Fatalf("unexpected detail: %q", detail)
	}

	detail2, err2 := f.FailureDetail("https://github.com/owner/repo/pull/8")
	if err2 != nil || detail2 != "" {
		t.Fatalf("want (\"\", nil) for unscripted URL; got (%q, %v)", detail2, err2)
	}
}

// TestFake_FailureDetailErr verifies the scripted error field.
func TestFake_FailureDetailErr(t *testing.T) {
	f := forge.NewFake()
	f.FailureDetailErr = errors.New("gh api graphql: 403 Forbidden")

	_, err := f.FailureDetail("https://github.com/owner/repo/pull/7")
	if err == nil || err.Error() != "gh api graphql: 403 Forbidden" {
		t.Fatalf("want scripted error, got %v", err)
	}
}

// TestFake_AgentBranch verifies the Fake concatenates its configured
// BranchPrefix with num, matching the real adapters' AgentBranch(num)
// contract (issue #444): the forge client owns the branch-prefix rule so
// call sites never concatenate it themselves.
func TestFake_AgentBranch(t *testing.T) {
	f := forge.NewFake()
	f.BranchPrefix = "agent/issue-"
	if got := f.AgentBranch("42"); got != "agent/issue-42" {
		t.Errorf("AgentBranch(42) = %q, want %q", got, "agent/issue-42")
	}
}

// TestFake_AgentBranch_ZeroValue verifies an unconfigured BranchPrefix
// ("") yields the bare issue number, matching an unconfigured
// config.branchPrefix's zero value.
func TestFake_AgentBranch_ZeroValue(t *testing.T) {
	f := forge.NewFake()
	if got := f.AgentBranch("7"); got != "7" {
		t.Errorf("AgentBranch(7) = %q, want %q", got, "7")
	}
}

// TestFake_ListPRFiles verifies that ListPRFiles returns the scripted changed
// files for a PR — the merge guard's only source of changed paths.
func TestFake_ListPRFiles(t *testing.T) {
	f := forge.NewFake()
	const url = "https://github.com/owner/repo/pull/42"
	f.SetPRFiles(url, []string{"src/main.go", ".github/workflows/ci.yml"})

	files, err := f.ListPRFiles(url)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"src/main.go", ".github/workflows/ci.yml"}
	if len(files) != len(want) {
		t.Fatalf("ListPRFiles = %v, want %v", files, want)
	}
	for i := range files {
		if files[i] != want[i] {
			t.Fatalf("ListPRFiles = %v, want %v", files, want)
		}
	}
}
