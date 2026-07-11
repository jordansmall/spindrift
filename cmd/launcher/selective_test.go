package main

import (
	"bytes"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// TestSelectiveListDispatch_AllLabeledNoPrompt: when all listed issues carry the
// ready-for-agent label no confirmation is needed and all are dispatched.
func TestSelectiveListDispatch_AllLabeledNoPrompt(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.maxParallel = 4

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "12", Title: "first", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "15", Title: "second", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "18", Title: "third", Labels: []string{c.label}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(c, fc, fc)

	stdin := &bytes.Buffer{}
	stdout := &bytes.Buffer{}

	err := selectiveListDispatch(c, fc, fc, dir, f, s, []string{"12", "15", "18"}, false, stdin, stdout)
	if err != nil {
		t.Fatalf("selectiveListDispatch: %v", err)
	}

	if len(fr.RunCalls) != 3 {
		t.Errorf("RunCalls: got %d, want 3", len(fr.RunCalls))
	}
	// No confirmation prompt was needed.
	if strings.Contains(stdout.String(), "[y/N]") {
		t.Errorf("unexpected confirmation prompt in output: %s", stdout.String())
	}
}

// TestSelectiveListDispatch_UnlabeledWarnsAndPrompts: unlabeled issue triggers
// warning and a single batched prompt; y confirms.
func TestSelectiveListDispatch_UnlabeledWarnsAndPrompts(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.maxParallel = 4

	fc := forge.NewFake()
	// #12 is labeled, #15 is not.
	fc.SetIssue(forge.Issue{Number: "12", Title: "labeled", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "15", Title: "unlabeled", Labels: []string{}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(c, fc, fc)

	stdin := strings.NewReader("y\n")
	stdout := &bytes.Buffer{}

	err := selectiveListDispatch(c, fc, fc, dir, f, s, []string{"12", "15"}, false, stdin, stdout)
	if err != nil {
		t.Fatalf("selectiveListDispatch: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "⚠") || !strings.Contains(out, "15") {
		t.Errorf("expected warning for #15, got: %s", out)
	}
	if !strings.Contains(out, "[y/N]") {
		t.Errorf("expected confirmation prompt, got: %s", out)
	}
	if len(fr.RunCalls) != 2 {
		t.Errorf("RunCalls: got %d, want 2 (both dispatched after confirmation)", len(fr.RunCalls))
	}
}

// TestSelectiveListDispatch_UnlabeledAbortOnN: answering n aborts with non-zero.
func TestSelectiveListDispatch_UnlabeledAbortOnN(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.maxParallel = 4

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "15", Title: "unlabeled", Labels: []string{}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(c, fc, fc)

	stdin := strings.NewReader("n\n")
	stdout := &bytes.Buffer{}

	err := selectiveListDispatch(c, fc, fc, dir, f, s, []string{"15"}, false, stdin, stdout)
	if err == nil {
		t.Fatal("expected error on abort, got nil")
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls: got %d, want 0 (no dispatch after abort)", len(fr.RunCalls))
	}
}

// TestSelectiveListDispatch_YesFlagSkipsPrompt: --yes skips the confirmation.
func TestSelectiveListDispatch_YesFlagSkipsPrompt(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.maxParallel = 4

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "15", Title: "unlabeled", Labels: []string{}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(c, fc, fc)

	stdin := &bytes.Buffer{} // no input; would hang if prompt fired
	stdout := &bytes.Buffer{}

	err := selectiveListDispatch(c, fc, fc, dir, f, s, []string{"15"}, true, stdin, stdout)
	if err != nil {
		t.Fatalf("selectiveListDispatch with --yes: %v", err)
	}
	if len(fr.RunCalls) != 1 {
		t.Errorf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
}

// TestSelectiveListDispatch_NonInteractiveAbort: no TTY and no --yes → abort.
func TestSelectiveListDispatch_NonInteractiveAbort(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.maxParallel = 4

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "15", Title: "unlabeled", Labels: []string{}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(c, fc, fc)

	stdin := &bytes.Buffer{} // EOF immediately = non-interactive
	stdout := &bytes.Buffer{}

	err := selectiveListDispatch(c, fc, fc, dir, f, s, []string{"15"}, false, stdin, stdout)
	if err == nil {
		t.Fatal("expected non-interactive abort error, got nil")
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls: got %d, want 0", len(fr.RunCalls))
	}
}

// TestSelectiveListDispatch_BlockerOrderedAhead: when #99 (already done — issue
// closed) blocks #15 and both are in the list, #15 is not evicted and both are
// dispatched.
func TestSelectiveListDispatch_BlockerOrderedAhead(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.maxParallel = 4
	c.depsPollSecs = 0
	c.depsWaitSecs = 100

	fc := forge.NewFake()
	// #99 is already done — issue closed (PR merged + auto-close).
	fc.SetIssue(forge.Issue{Number: "99", Title: "blocker", State: "CLOSED", Labels: []string{c.label}})
	// #15 is blocked by #99 (in the list and closed → edge satisfied).
	fc.SetIssue(forge.Issue{Number: "15", Title: "dependent", Labels: []string{c.label},
		Body: "## Blocked by\n- #99\n"})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(c, fc, fc)
	stdin := &bytes.Buffer{}
	stdout := &bytes.Buffer{}

	err := selectiveListDispatch(c, fc, fc, dir, f, s, []string{"15", "99"}, false, stdin, stdout)
	if err != nil {
		t.Fatalf("selectiveListDispatch: %v", err)
	}

	// Both should be dispatched.
	if len(fr.RunCalls) != 2 {
		t.Errorf("RunCalls: got %d, want 2 (in-list blocker must not cause eviction)", len(fr.RunCalls))
	}
}

// TestSelectiveListDispatch_InListUnmergedBlocker_DispatchesOnlyBlocker is
// the regression test for #524's acceptance criterion: `dispatch 12 15`
// where #15 is blocked by in-list, unmerged #12 dispatches #12 only in one
// invocation; #15 is not claimed. The exact remaining-list/re-run-command
// output is covered at the waves-package level (drainMaxJobs writes it to
// stdout directly, not through the io.Writer this test's caller injects).
func TestSelectiveListDispatch_InListUnmergedBlocker_DispatchesOnlyBlocker(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.maxParallel = 4

	fc := forge.NewFake()
	// #12 is open (not merged/closed) and blocks #15; both are in the list.
	fc.SetIssue(forge.Issue{Number: "12", Title: "blocker", State: "OPEN", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "15", Title: "dependent", Labels: []string{c.label},
		Body: "## Blocked by\n- #12\n"})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(c, fc, fc)
	stdin := &bytes.Buffer{}
	stdout := &bytes.Buffer{}

	err := selectiveListDispatch(c, fc, fc, dir, f, s, []string{"12", "15"}, false, stdin, stdout)
	if err != nil {
		t.Fatalf("selectiveListDispatch: %v", err)
	}

	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "12" {
		t.Fatalf("RunCalls: got %v, want exactly issue 12 (dependent must wait for a fresh invocation)", fr.RunCalls)
	}

	iss15, err := fc.Issue("15")
	if err != nil {
		t.Fatalf("Issue(15): %v", err)
	}
	if containsLabel(iss15.Labels, c.inProgressLabel) {
		t.Errorf("issue 15 must not be claimed while its blocker is unmet; labels=%v", iss15.Labels)
	}
}

// TestSelectiveListDispatch_UnmetExternalEviction: #15 is blocked by #99 (not
// in list, not merged) — #15 is evicted and nothing is dispatched.
func TestSelectiveListDispatch_UnmetExternalEviction(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.maxParallel = 4

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "15", Title: "dependent", Labels: []string{c.label},
		Body: "## Blocked by\n- #99\n"})
	fc.SetIssue(forge.Issue{Number: "99", State: "OPEN", Labels: []string{}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(c, fc, fc)
	stdin := &bytes.Buffer{}
	stdout := &bytes.Buffer{}

	err := selectiveListDispatch(c, fc, fc, dir, f, s, []string{"15"}, false, stdin, stdout)
	if err != nil {
		t.Fatalf("selectiveListDispatch: %v", err)
	}

	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls: got %d, want 0 (evicted issue must not be dispatched)", len(fr.RunCalls))
	}
	if !strings.Contains(stdout.String(), "99") {
		t.Errorf("output should mention unmet blocker #99, got: %s", stdout.String())
	}
}

// TestPreviewIssues_WithList_ShowsAnnotations: when a list of issue numbers is
// given, preview shows each issue with its blockers annotated inline.
func TestPreviewIssues_WithList_ShowsAnnotations(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.repoSlug = "owner/repo"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "99", Title: "blocker issue", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "15", Title: "dependent", Labels: []string{c.label},
		Body: "## Blocked by\n- #99\n"})

	var buf bytes.Buffer
	if err := previewIssues(c, fc, fc, &buf, []string{"15", "99"}, t.TempDir(), nil); err != nil {
		t.Fatalf("previewIssues: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "#15") {
		t.Errorf("output missing #15; got:\n%s", out)
	}
	if !strings.Contains(out, "#99") {
		t.Errorf("output missing #99; got:\n%s", out)
	}
	// #15 should show its blocker annotation.
	if !strings.Contains(out, "blocked by #99") {
		t.Errorf("output missing blocker annotation for #15; got:\n%s", out)
	}
}

// TestPreviewIssues_WithList_ShowsEviction: an issue evicted due to unmet
// external blocker is shown with a notice (not included in would-dispatch list).
func TestPreviewIssues_WithList_ShowsEviction(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.repoSlug = "owner/repo"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "15", Title: "dependent", Labels: []string{c.label},
		Body: "## Blocked by\n- #200\n"})
	fc.SetIssue(forge.Issue{Number: "200", State: "OPEN", Labels: []string{}})

	var buf bytes.Buffer
	if err := previewIssues(c, fc, fc, &buf, []string{"15"}, t.TempDir(), nil); err != nil {
		t.Fatalf("previewIssues: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "200") {
		t.Errorf("output missing unmet blocker #200 in eviction notice; got:\n%s", out)
	}
}

// TestPreviewIssues_WithList_ShowsUnlabeledWarning: unlabeled issue gets a
// warning but no confirmation prompt.
func TestPreviewIssues_WithList_ShowsUnlabeledWarning(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.repoSlug = "owner/repo"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "15", Title: "unlabeled", Labels: []string{}})

	var buf bytes.Buffer
	if err := previewIssues(c, fc, fc, &buf, []string{"15"}, t.TempDir(), nil); err != nil {
		t.Fatalf("previewIssues: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "⚠") || !strings.Contains(out, "15") {
		t.Errorf("output missing unlabeled warning for #15; got:\n%s", out)
	}
	// No prompt in preview mode.
	if strings.Contains(out, "[y/N]") {
		t.Errorf("preview must not prompt; got:\n%s", out)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("previewIssues made TransitionState calls; want 0")
	}
}

// TestPreviewIssues_WithList_NoMutatingCalls: preview with list makes no
// mutating forge calls (no TransitionState, no Comment).
func TestPreviewIssues_WithList_NoMutatingCalls(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.repoSlug = "owner/repo"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "12", Title: "first", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "15", Title: "second", Labels: []string{c.label}})

	var buf bytes.Buffer
	if err := previewIssues(c, fc, fc, &buf, []string{"12", "15"}, t.TempDir(), nil); err != nil {
		t.Fatalf("previewIssues: %v", err)
	}

	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("previewIssues made %d TransitionState calls; want 0", len(fc.TransitionStateCalls))
	}
	if len(fc.CommentCalls) != 0 {
		t.Errorf("previewIssues made %d Comment calls; want 0", len(fc.CommentCalls))
	}
}

// TestEvictUnmetBlockers_EvictsExternalUnmet verifies that an issue whose only
// blocker is NOT in the list (and not yet merged) is evicted with a notice.
func TestEvictUnmetBlockers_EvictsExternalUnmet(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	// #15 is in the list; it is blocked by #99 which is NOT in the list and is open.
	fc.SetIssue(forge.Issue{Number: "15", Title: "needs 99", Labels: []string{}})
	fc.SetIssue(forge.Issue{Number: "99", State: "OPEN", Labels: []string{}})

	issues := []issue{{number: "15", title: "needs 99"}}
	edges := map[string][]string{"15": {"99"}}

	kept, notices := evictUnmetBlockers(c, fc, fc, issues, edges)

	if len(kept) != 0 {
		t.Errorf("kept = %v, want empty (issue should be evicted)", kept)
	}
	if len(notices) != 1 {
		t.Fatalf("notices = %v, want 1 notice", notices)
	}
	if !strings.Contains(notices[0], "15") || !strings.Contains(notices[0], "99") {
		t.Errorf("notice should mention #15 and #99, got: %s", notices[0])
	}
}

// TestEvictUnmetBlockers_KeepsInListBlocker verifies that when the blocker is
// also in the list the dependent is retained (will be ordered behind it).
func TestEvictUnmetBlockers_KeepsInListBlocker(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "10", Title: "blocker", Labels: []string{}})
	fc.SetIssue(forge.Issue{Number: "15", Title: "depends on 10", Labels: []string{}})

	// Both in the list; #15 blocked by #10 (also in list).
	issues := []issue{
		{number: "10", title: "blocker"},
		{number: "15", title: "depends on 10"},
	}
	edges := map[string][]string{"15": {"10"}}

	kept, notices := evictUnmetBlockers(c, fc, fc, issues, edges)

	if len(kept) != 2 {
		t.Errorf("kept = %v, want 2 issues (both should survive)", kept)
	}
	if len(notices) != 0 {
		t.Errorf("notices = %v, want none", notices)
	}
}

// TestEvictUnmetBlockers_KeepsMergedBlocker verifies that a closed/merged
// external blocker (not in list) satisfies the edge (no eviction).
func TestEvictUnmetBlockers_KeepsMergedBlocker(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "15", Title: "needs 99", Labels: []string{}})
	// #99 is CLOSED (merged) — satisfies the edge even though not in list.
	fc.SetIssue(forge.Issue{Number: "99", State: "CLOSED", Labels: []string{}})

	issues := []issue{{number: "15", title: "needs 99"}}
	edges := map[string][]string{"15": {"99"}}

	kept, notices := evictUnmetBlockers(c, fc, fc, issues, edges)

	if len(kept) != 1 {
		t.Errorf("kept = %v, want [#15] (closed blocker satisfies edge)", kept)
	}
	if len(notices) != 0 {
		t.Errorf("notices = %v, want none", notices)
	}
}

// TestEvictUnmetBlockers_CascadingEviction verifies that when A is evicted
// because of an unmet external blocker, B (which depends on A) is also evicted.
func TestEvictUnmetBlockers_CascadingEviction(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	// #99 blocks #10 (both in list); but #99 is blocked by #200 (external, unmet).
	fc.SetIssue(forge.Issue{Number: "10", Title: "depends on 99", Labels: []string{}})
	fc.SetIssue(forge.Issue{Number: "99", Title: "depends on 200", Labels: []string{}})
	fc.SetIssue(forge.Issue{Number: "200", State: "OPEN", Labels: []string{}})

	issues := []issue{
		{number: "99", title: "depends on 200"},
		{number: "10", title: "depends on 99"},
	}
	edges := map[string][]string{
		"99": {"200"}, // external unmet blocker
		"10": {"99"},  // in-list blocker (but 99 will be evicted)
	}

	kept, notices := evictUnmetBlockers(c, fc, fc, issues, edges)

	if len(kept) != 0 {
		t.Errorf("kept = %v, want empty (both should cascade-evict)", kept)
	}
	if len(notices) < 2 {
		t.Errorf("notices = %v, want at least 2 (one per evicted issue)", notices)
	}
}
