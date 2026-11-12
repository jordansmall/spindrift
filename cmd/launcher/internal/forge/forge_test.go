package forge_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/forge"
)

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

	s3, err := f.CheckState(url)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s3 != forge.StateNone {
		t.Fatalf("poll 3 (exhausted): want NONE, got %q", s3)
	}
}

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

func TestFake_AddLabels(t *testing.T) {
	t.Run("appends missing labels, dedups already-present ones", func(t *testing.T) {
		f := forge.NewFake()
		f.SetIssue(forge.Issue{Number: "42", Labels: []string{"agent-butler-finding"}})

		if err := f.AddLabels("42", []string{"agent-butler-finding", "ready-for-agent"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(f.AddLabelsCalls) != 1 {
			t.Fatalf("want 1 AddLabelsCall, got %d", len(f.AddLabelsCalls))
		}
		iss, err := f.Issue("42")
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		want := []string{"agent-butler-finding", "ready-for-agent"}
		if len(iss.Labels) != len(want) {
			t.Fatalf("labels = %v, want %v", iss.Labels, want)
		}
		for _, l := range want {
			found := false
			for _, got := range iss.Labels {
				if got == l {
					found = true
				}
			}
			if !found {
				t.Errorf("labels = %v, missing %q", iss.Labels, l)
			}
		}
	})

	t.Run("errors on unknown issue", func(t *testing.T) {
		f := forge.NewFake()
		if err := f.AddLabels("no-such-issue", []string{"ready-for-agent"}); err == nil {
			t.Fatal("want error for unknown issue, got nil")
		}
	})

	t.Run("returns scripted error", func(t *testing.T) {
		f := forge.NewFake()
		f.SetIssue(forge.Issue{Number: "42"})
		f.AddLabelsErr = errors.New("api error")

		err := f.AddLabels("42", []string{"ready-for-agent"})
		if err == nil || err.Error() != "api error" {
			t.Fatalf("want api error, got %v", err)
		}
	})
}

func TestFake_OpenPRForBranch(t *testing.T) {
	f := forge.NewFake()
	f.SetPR("agent/issue-7", forge.PR{URL: "https://github.com/o/r/pull/99"})

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

func TestFake_MarkReadyMarkDraft(t *testing.T) {
	const url = "https://github.com/o/r/pull/99"
	// A scripted failure must surface and leave the draft state untouched.
	cases := []struct {
		name      string
		seedDraft bool
		script    func(f *forge.Fake) error
		call      func(f *forge.Fake) error
		calls     func(f *forge.Fake) []string
		wantDraft bool
		wantErr   bool
	}{
		{
			name:      "MarkReady",
			seedDraft: true,
			call:      func(f *forge.Fake) error { return f.MarkReady(url) },
			calls:     func(f *forge.Fake) []string { return f.MarkReadyCalls },
			wantDraft: false,
		},
		{
			name:      "MarkDraft",
			call:      func(f *forge.Fake) error { return f.MarkDraft(url) },
			calls:     func(f *forge.Fake) []string { return f.MarkDraftCalls },
			wantDraft: true,
		},
		{
			name:      "MarkReadyErr",
			seedDraft: true,
			script: func(f *forge.Fake) error {
				f.MarkReadyErr = errors.New("gh pr ready: 502")
				return f.MarkReadyErr
			},
			call:      func(f *forge.Fake) error { return f.MarkReady(url) },
			wantDraft: true,
			wantErr:   true,
		},
		{
			name: "MarkDraftErr",
			script: func(f *forge.Fake) error {
				f.MarkDraftErr = errors.New("gh pr ready --undo: 502")
				return f.MarkDraftErr
			},
			call:      func(f *forge.Fake) error { return f.MarkDraft(url) },
			wantDraft: false,
			wantErr:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := forge.NewFake()
			f.SetPR("agent/issue-7", forge.PR{URL: url})
			if tc.seedDraft {
				f.SetDraft(url, true)
			}
			var scripted error
			if tc.script != nil {
				scripted = tc.script(f)
			}
			if got := f.IsDraft(url); got != tc.seedDraft {
				t.Fatalf("IsDraft(%q) = %v before call, want %v", url, got, tc.seedDraft)
			}

			err := tc.call(f)
			if tc.wantErr {
				if !errors.Is(err, scripted) {
					t.Fatalf("err = %v, want %v", err, scripted)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got := tc.calls(f); len(got) != 1 || got[0] != url {
					t.Fatalf("want calls=[%q], got %v", url, got)
				}
			}
			if got := f.IsDraft(url); got != tc.wantDraft {
				t.Fatalf("IsDraft(%q) = %v after call, want %v", url, got, tc.wantDraft)
			}
		})
	}
}

// An unscripted URL must return "" and no error: the fetch is best-effort, so
// a PR with nothing scripted must not look like a hard failure.
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

func TestFake_FailureDetailErr(t *testing.T) {
	f := forge.NewFake()
	f.FailureDetailErr = errors.New("gh api graphql: 403 Forbidden")

	_, err := f.FailureDetail("https://github.com/owner/repo/pull/7")
	if err == nil || err.Error() != "gh api graphql: 403 Forbidden" {
		t.Fatalf("want scripted error, got %v", err)
	}
}

// AgentBranchName is plain concatenation: the prefix carries the whole shape
// (BRANCH_PREFIX defaults to "agent/issue-"), so a custom prefix is honored
// verbatim rather than re-suffixed.
func TestAgentBranchName(t *testing.T) {
	for _, tc := range []struct{ prefix, num, want string }{
		{"agent/issue-", "42", "agent/issue-42"},
		{"bot/", "7", "bot/7"},
		{"", "9", "9"},
	} {
		if got := forge.AgentBranchName(tc.prefix, tc.num); got != tc.want {
			t.Errorf("AgentBranchName(%q, %q) = %q, want %q", tc.prefix, tc.num, got, tc.want)
		}
	}
}

// Every adapter's Merge failure path shares this mapping.
func TestClassifyMergeFailure(t *testing.T) {
	cases := []struct {
		name    string
		state   forge.MergeableState
		wantErr error
		wantOK  bool
	}{
		{"conflicting", forge.MergeableConflicting, forge.ErrMergeConflict, true},
		{"mergeable", forge.MergeableMergeable, forge.ErrMergeBlockedByChecks, true},
		{"unknown", forge.MergeableUnknown, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err, ok := forge.ClassifyMergeFailure(tc.state)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !errors.Is(err, tc.wantErr) && err != tc.wantErr {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestRenderFailureDetail(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if got := forge.RenderFailureDetail(nil); got != "" {
			t.Fatalf("want \"\", got %q", got)
		}
	})

	t.Run("formats entries", func(t *testing.T) {
		entries := []forge.FailureDetailEntry{
			{Name: "lint", State: "FAILURE", Summary: "2 errors"},
			{Name: "legacy-status", State: "ERROR", Summary: "build broke"},
		}
		got := forge.RenderFailureDetail(entries)
		want := "lint: FAILURE\n2 errors\n---\nlegacy-status: ERROR\nbuild broke\n---"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("omits blank summary", func(t *testing.T) {
		entries := []forge.FailureDetailEntry{
			{Name: "legacy-ci", State: "FAILURE"},
		}
		got := forge.RenderFailureDetail(entries)
		want := "legacy-ci: FAILURE\n---"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("caps a huge summary to its head", func(t *testing.T) {
		entries := []forge.FailureDetailEntry{
			{Name: "huge", State: "FAILURE", Summary: strings.Repeat("x", forge.MaxFailureDetailBytes*2)},
		}
		got := forge.RenderFailureDetail(entries)
		want := "huge: FAILURE\n" + strings.Repeat("x", forge.MaxSummaryBytes) + "...[cut]\n---"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("an oversized summary leaves every job a log excerpt", func(t *testing.T) {
		entries := []forge.FailureDetailEntry{
			{Name: "third-party", State: "FAILURE", Summary: strings.Repeat("s", 64<<10)},
		}
		for _, n := range []string{"job-1", "job-2", "job-3"} {
			entries = append(entries, forge.FailureDetailEntry{
				Name: n, State: "FAILURE",
				Log: strings.Repeat("noise line\n", forge.MaxLogExcerptBytes) + "ERROR " + n,
			})
		}
		got := forge.RenderFailureDetail(entries)
		if len(got) > forge.MaxFailureDetailBytes {
			t.Fatalf("len(got) = %d, want <= %d", len(got), forge.MaxFailureDetailBytes)
		}
		if strings.Contains(got, "last 0 bytes") {
			t.Fatalf("a log was starved to nothing:\n%s", got)
		}
		for _, n := range []string{"job-1", "job-2", "job-3"} {
			if want := "ERROR " + n + "\n"; !strings.Contains(got, want) {
				t.Errorf("missing final line %q", want)
			}
		}
	})

	t.Run("a very long name stays within the bound", func(t *testing.T) {
		entries := []forge.FailureDetailEntry{
			{Name: strings.Repeat("n", forge.MaxFailureDetailBytes*2), State: "FAILURE", Log: strings.Repeat("log\n", 5000) + "ERROR end"},
			{Name: strings.Repeat("世", forge.MaxFailureDetailBytes), State: "FAILURE", Log: "ERROR two"},
		}
		got := forge.RenderFailureDetail(entries)
		if len(got) > forge.MaxFailureDetailBytes {
			t.Fatalf("len(got) = %d, want <= %d", len(got), forge.MaxFailureDetailBytes)
		}
		if !utf8.ValidString(got) {
			t.Fatal("invalid UTF-8")
		}
		if !strings.Contains(got, "ERROR end\n") || !strings.Contains(got, "ERROR two\n") {
			t.Fatalf("lost a log tail:\n%s", got)
		}
	})

	t.Run("shows the log excerpt after the summary", func(t *testing.T) {
		entries := []forge.FailureDetailEntry{
			{Name: "build", State: "FAILURE", Summary: "1 error", Log: "compiling\nerror: boom\n"},
		}
		got := forge.RenderFailureDetail(entries)
		want := "build: FAILURE\n1 error\ncompiling\nerror: boom\n---"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("trims a long log to its tail and names the job", func(t *testing.T) {
		log := "first\n" + strings.Repeat("a", 3000) + "\n" + strings.Repeat("b", 3000) + "\nlast line"
		got := forge.RenderFailureDetail([]forge.FailureDetailEntry{
			{Name: "build", State: "FAILURE", Log: log},
		})
		want := "build: FAILURE\n" +
			"[log excerpt for \"build\" trimmed to its last 4000 bytes or fewer]\n" +
			strings.Repeat("b", 3000) + "\nlast line\n---"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("keeps a single overlong line's tail", func(t *testing.T) {
		got := forge.RenderFailureDetail([]forge.FailureDetailEntry{
			{Name: "build", State: "FAILURE", Log: strings.Repeat("x", 5000)},
		})
		want := "build: FAILURE\n" +
			"[log excerpt for \"build\" trimmed to its last 4000 bytes or fewer]\n" +
			strings.Repeat("x", 4000) + "\n---"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("log excerpt cut lands on a rune boundary", func(t *testing.T) {
		// 1334 three-byte runes plus one ASCII byte is 4003 bytes, so the
		// 4000-byte tail would start one byte into the first rune.
		log := strings.Repeat("世", 1334) + "x"
		got := forge.RenderFailureDetail([]forge.FailureDetailEntry{
			{Name: "build", State: "FAILURE", Log: log},
		})
		want := "build: FAILURE\n" +
			"[log excerpt for \"build\" trimmed to its last 4000 bytes or fewer]\n" +
			strings.Repeat("世", 1333) + "x\n---"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("invalid UTF-8: %q", got)
		}
	})

	t.Run("omission line stays in budget for cut names that %q expands", func(t *testing.T) {
		// capHead appends "...[cut]" to an over-long name, so the omission
		// line names a string longer than MaxNameBytes. How many entries fit
		// depends on the summary size alone, and an overflow needs the output
		// to land within that slack of the bound, so sweep every summary size
		// for the digit widths of the omitted count rather than every count.
		name := strings.Repeat("\x00", forge.MaxNameBytes+1)
		for _, count := range []int{12, 60} {
			for sum := 0; sum <= forge.MaxSummaryBytes; sum++ {
				entries := make([]forge.FailureDetailEntry, count)
				for i := range entries {
					entries[i] = forge.FailureDetailEntry{Name: name, State: "FAILURE", Summary: strings.Repeat("s", sum)}
				}
				if got := forge.RenderFailureDetail(entries); len(got) > forge.MaxFailureDetailBytes {
					t.Fatalf("count=%d summary=%d: len(got) = %d, want <= %d", count, sum, len(got), forge.MaxFailureDetailBytes)
				}
			}
		}
	})

	t.Run("notices a trim hidden by trailing whitespace", func(t *testing.T) {
		log := strings.Repeat("noise line\n", forge.JobLogKeepBytes)[:forge.JobLogKeepBytes-5000] + strings.Repeat(" \n", 2500)
		if len(log) != forge.JobLogKeepBytes {
			t.Fatalf("setup: len(log) = %d", len(log))
		}
		got := forge.RenderFailureDetail([]forge.FailureDetailEntry{{Name: "padded", State: "FAILURE", Log: log}})
		if want := fmt.Sprintf("[log excerpt for %q trimmed to its last ", "padded"); !strings.Contains(got, want) {
			t.Fatalf("missing trim notice:\n%.300s", got)
		}
	})

	t.Run("keeps content ahead of a whitespace-only tail", func(t *testing.T) {
		log := strings.Repeat("noise line\n", 1000) + "real final line" + strings.Repeat(" \n", forge.MaxLogExcerptBytes)
		got := forge.RenderFailureDetail([]forge.FailureDetailEntry{{Name: "padded", State: "FAILURE", Log: log}})
		if !strings.Contains(got, "real final line\n---") {
			t.Fatalf("excerpt lost the content before the whitespace:\n%.400s", got)
		}
		if want := fmt.Sprintf("[log excerpt for %q trimmed to its last ", "padded"); !strings.Contains(got, want) {
			t.Fatalf("missing trim notice:\n%.300s", got)
		}
	})

	t.Run("budgets excerpts so every job keeps its final line", func(t *testing.T) {
		var entries []forge.FailureDetailEntry
		for _, n := range []string{"job-1", "job-2", "job-3", "job-4"} {
			entries = append(entries, forge.FailureDetailEntry{
				Name:  n,
				State: "FAILURE",
				Log:   strings.Repeat("noise line\n", forge.MaxLogExcerptBytes) + "ERROR " + n,
			})
		}
		got := forge.RenderFailureDetail(entries)
		if len(got) > forge.MaxFailureDetailBytes {
			t.Fatalf("len(got) = %d, want <= %d", len(got), forge.MaxFailureDetailBytes)
		}
		if strings.Contains(got, "failure detail trimmed") {
			t.Fatalf("budgeting should avoid the overall cut:\n%s", got)
		}
		for _, e := range entries {
			if want := "ERROR " + e.Name + "\n"; !strings.Contains(got, want) {
				t.Errorf("missing final line %q", want)
			}
			if want := fmt.Sprintf("[log excerpt for %q trimmed to its last ", e.Name); !strings.Contains(got, want) {
				t.Errorf("missing trim notice naming %q", e.Name)
			}
		}
		// Four logs share the budget, so the cap stated is under MaxLogExcerptBytes.
		if full := fmt.Sprintf("last %d bytes", forge.MaxLogExcerptBytes); strings.Contains(got, full) {
			t.Errorf("notice states the %d-byte cap although the budget forced less:\n%s", forge.MaxLogExcerptBytes, got[:300])
		}
	})

	t.Run("omits trailing entries whole and names the first", func(t *testing.T) {
		var entries []forge.FailureDetailEntry
		for i := range 40 {
			entries = append(entries, forge.FailureDetailEntry{
				Name:  fmt.Sprintf("job-%02d", i),
				State: "FAILURE",
				Log:   strings.Repeat("noise line\n", forge.MaxLogExcerptBytes) + fmt.Sprintf("ERROR job-%02d", i),
			})
		}
		got := forge.RenderFailureDetail(entries)
		if len(got) > forge.MaxFailureDetailBytes {
			t.Fatalf("len(got) = %d, want <= %d", len(got), forge.MaxFailureDetailBytes)
		}
		var kept int
		for i := range entries {
			if strings.Contains(got, fmt.Sprintf("ERROR job-%02d\n", i)) {
				kept++
			} else if !strings.Contains(got, fmt.Sprintf("job-%02d: ", i)) {
				break
			}
		}
		if kept == 0 || kept == len(entries) {
			t.Fatalf("kept %d of %d entries, want a strict prefix", kept, len(entries))
		}
		want := fmt.Sprintf("[%d further failing entries omitted to fit %d bytes, starting with \"job-%02d\"]",
			len(entries)-kept, forge.MaxFailureDetailBytes, kept)
		if !strings.HasSuffix(got, want) {
			t.Fatalf("tail = %q, want suffix %q", got[len(got)-150:], want)
		}
		if strings.Contains(got, fmt.Sprintf("job-%02d: ", kept)) {
			t.Fatalf("entry %d should have been omitted whole", kept)
		}
		if strings.Contains(got, "last 0 bytes") {
			t.Fatal("a log was starved to nothing")
		}
	})

	t.Run("capped summary stays valid UTF-8", func(t *testing.T) {
		entries := []forge.FailureDetailEntry{
			{Name: "huge", State: "FAILURE", Summary: strings.Repeat("世", forge.MaxFailureDetailBytes)},
		}
		got := forge.RenderFailureDetail(entries)
		if !strings.Contains(got, "...[cut]\n---") {
			t.Fatalf("missing cut marker, got %q", got[len(got)-40:])
		}
		if !utf8.ValidString(got) {
			t.Fatal("invalid UTF-8 after the summary cap")
		}
	})
}

// ADR 0040 requires an unlabeled issue to default to Normal without any adapter
// setting it explicitly, so the zero value has to be PriorityNormal.
func TestPriority_ZeroValue(t *testing.T) {
	var p forge.Priority
	if p != forge.PriorityNormal {
		t.Fatalf("zero value = %v, want PriorityNormal", p)
	}
}

// ADR 0040's central sort needs Critical > High > Normal > Low to hold under
// plain Go comparison operators.
func TestPriority_Ordering(t *testing.T) {
	if !(forge.PriorityCritical > forge.PriorityHigh) {
		t.Errorf("want PriorityCritical > PriorityHigh")
	}
	if !(forge.PriorityHigh > forge.PriorityNormal) {
		t.Errorf("want PriorityHigh > PriorityNormal")
	}
	if !(forge.PriorityNormal > forge.PriorityLow) {
		t.Errorf("want PriorityNormal > PriorityLow")
	}
	if !(forge.PriorityCritical > forge.PriorityLow) {
		t.Errorf("want PriorityCritical > PriorityLow")
	}
}

func TestPriority_String(t *testing.T) {
	cases := []struct {
		p    forge.Priority
		want string
	}{
		{forge.PriorityCritical, "critical"},
		{forge.PriorityHigh, "high"},
		{forge.PriorityNormal, "normal"},
		{forge.PriorityLow, "low"},
	}
	for _, c := range cases {
		if got := c.p.String(); got != c.want {
			t.Errorf("Priority(%d).String() = %q, want %q", int(c.p), got, c.want)
		}
	}
}

// Labels, not a separately-set Priority field, is the single source of truth:
// the Fake resolves Priority at read time so a test that mutates Labels via
// SetIssue cannot leave the two out of sync. All three read paths must agree.
func TestFake_ResolvesPriorityFromLabels(t *testing.T) {
	f := forge.NewFake(testLabels)
	f.SetIssue(forge.Issue{
		Number: "42",
		Labels: []string{"ready-for-agent", "agent-priority-high"},
	})

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss.Priority != forge.PriorityHigh {
		t.Errorf("Issue(42).Priority = %v, want PriorityHigh", iss.Priority)
	}

	listed, err := f.ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(listed) != 1 || listed[0].Priority != forge.PriorityHigh {
		t.Fatalf("ListIssues(Dispatchable) = %+v, want one issue with PriorityHigh", listed)
	}

	open, err := f.ListOpenIssues()
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	if len(open) != 1 || open[0].Priority != forge.PriorityHigh {
		t.Fatalf("ListOpenIssues() = %+v, want one issue with PriorityHigh", open)
	}
}

// ListPRFiles is the merge guard's only source of changed paths.
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

func TestRateLimitError_MatchesSentinelAndCarriesReset(t *testing.T) {
	reset := time.Unix(1900000000, 0)
	inner := errors.New("gh api: boom")
	err := &forge.RateLimitError{Reset: reset, Err: inner}
	wrapped := fmt.Errorf("probe: %w", err)

	if !errors.Is(wrapped, forge.ErrRateLimit) {
		t.Error("wrapped RateLimitError does not match ErrRateLimit")
	}
	if !errors.Is(wrapped, inner) {
		t.Error("wrapped RateLimitError does not unwrap to its cause")
	}
	var rl *forge.RateLimitError
	if !errors.As(wrapped, &rl) || !rl.Reset.Equal(reset) {
		t.Errorf("errors.As recovered %+v, want Reset %v", rl, reset)
	}
	if got, want := err.Error(), "forge rate limited: gh api: boom"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if got := (&forge.RateLimitError{}).Error(); got != forge.ErrRateLimit.Error() {
		t.Errorf("bare Error() = %q", got)
	}
}
