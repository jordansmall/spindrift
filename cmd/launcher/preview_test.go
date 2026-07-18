package main

import (
	"bytes"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/waves"
)

// TestPreviewIssues_ListsIssuesAndRepo verifies that previewIssues prints the
// candidate issues and target repo without making any mutating Forge calls.
func TestPreviewIssues_ListsIssuesAndRepo(t *testing.T) {
	c := baseConfig()
	c.repoSlug = "owner/repo"
	c.label = "ready-for-agent"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "10", Title: "first issue", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "20", Title: "second issue", Labels: []string{c.label}})

	var buf bytes.Buffer
	if err := previewIssues(c, fc, fc, &buf, nil, t.TempDir(), nil); err != nil {
		t.Fatalf("previewIssues: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "owner/repo") {
		t.Errorf("output missing repo slug; got:\n%s", out)
	}
	if !strings.Contains(out, "#10") {
		t.Errorf("output missing issue #10; got:\n%s", out)
	}
	if !strings.Contains(out, "#20") {
		t.Errorf("output missing issue #20; got:\n%s", out)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("previewIssues made %d TransitionState calls; want 0", len(fc.TransitionStateCalls))
	}
	if len(fc.CommentCalls) != 0 {
		t.Errorf("previewIssues made %d Comment calls; want 0", len(fc.CommentCalls))
	}
}

// TestPreviewIssues_PrintsMergeMode verifies that previewIssues prints the
// effective merge mode so the operator sees which mode is armed.
func TestPreviewIssues_PrintsMergeMode(t *testing.T) {
	c := baseConfig()
	c.repoSlug = "owner/repo"
	c.label = "ready-for-agent"
	c.mergeMode = "immediate"
	fc := forge.NewFake()

	var buf bytes.Buffer
	if err := previewIssues(c, fc, fc, &buf, nil, t.TempDir(), nil); err != nil {
		t.Fatalf("previewIssues: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "immediate") {
		t.Errorf("previewIssues output must include merge mode; got:\n%s", out)
	}
}

// TestPrintPlan_AnnotatesBlockers verifies that printPlan — the single shared
// blocker-annotation printer used by both the discovered-batch and selective
// preview paths — prints the dispatch count and annotates only the issues
// that carry blockers.
func TestPrintPlan_AnnotatesBlockers(t *testing.T) {
	plan := waves.Plan{
		Issues: []waves.Issue{
			{Number: "99", Title: "blocker issue"},
			{Number: "15", Title: "dependent"},
		},
		Edges:   map[string][]string{"15": {"99"}},
		Sources: waves.Sources{"15": {"99": forge.DepSourceNative}},
	}

	var buf bytes.Buffer
	printPlan(&buf, plan)

	out := buf.String()
	if !strings.Contains(out, "2 issue(s) would be dispatched") {
		t.Errorf("output missing dispatch count; got:\n%s", out)
	}
	if !strings.Contains(out, "#15  dependent  (blocked by #99 (native))") {
		t.Errorf("output missing blocker annotation for #15; got:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "blocker issue") && strings.Contains(line, "blocked by") {
			t.Errorf("#99 line should not have blocker annotation; got: %s", line)
		}
	}
}

// TestPrintHelp_ShowsPreview verifies that --help lists the preview subcommand.
func TestPrintHelp_ShowsPreview(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	if !strings.Contains(buf.String(), "preview") {
		t.Error("help output missing 'preview' subcommand")
	}
}

// TestPreviewIssues_EmptyQueue verifies that an empty issue queue is reported
// cleanly and does not error.
func TestPreviewIssues_EmptyQueue(t *testing.T) {
	c := baseConfig()
	c.repoSlug = "owner/repo"
	c.label = "ready-for-agent"
	fc := forge.NewFake()

	var buf bytes.Buffer
	if err := previewIssues(c, fc, fc, &buf, nil, t.TempDir(), nil); err != nil {
		t.Fatalf("previewIssues: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "owner/repo") {
		t.Errorf("output missing repo; got:\n%s", out)
	}
	if !strings.Contains(out, "nothing to dispatch") {
		t.Errorf("output should mention nothing to dispatch; got:\n%s", out)
	}
}

// TestPreviewIssues_PrintsImageFreshnessLine verifies that previewIssues
// surfaces the image-freshness probe result as its own line — bwrap has no
// loaded image, so it must report "not applicable" rather than attempting a
// fetch or eval.
func TestPreviewIssues_PrintsImageFreshnessLine(t *testing.T) {
	c := baseConfig()
	c.repoSlug = "owner/repo"
	c.label = "ready-for-agent"
	c.runtime = "bwrap"
	fc := forge.NewFake()

	var buf bytes.Buffer
	if err := previewIssues(c, fc, fc, &buf, nil, "/unused", nil); err != nil {
		t.Fatalf("previewIssues: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "image-freshness:") {
		t.Errorf("output missing image-freshness line; got:\n%s", out)
	}
	if !strings.Contains(out, "not applicable") {
		t.Errorf("output missing not-applicable freshness message for bwrap; got:\n%s", out)
	}
}

// TestPreviewIssues_BareAnnotatesBlockers verifies that bare preview (no
// positionals) annotates each issue with its inline blocker references.
func TestPreviewIssues_BareAnnotatesBlockers(t *testing.T) {
	c := baseConfig()
	c.repoSlug = "owner/repo"
	c.label = "ready-for-agent"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "99", Title: "blocker issue", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "15", Title: "dependent", Labels: []string{c.label},
		Body: "## Blocked by\n- #99\n"})

	var buf bytes.Buffer
	if err := previewIssues(c, fc, fc, &buf, nil, t.TempDir(), nil); err != nil {
		t.Fatalf("previewIssues: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "#15") {
		t.Errorf("output missing #15; got:\n%s", out)
	}
	// #15 must show its blocker annotation, sourced from body-text parsing
	// (the Fake's blocker ref came from #15's "## Blocked by" section, not
	// NativeDeps).
	if !strings.Contains(out, "blocked by #99 (body)") {
		t.Errorf("output missing body-sourced blocker annotation for #15; got:\n%s", out)
	}
	// #99 has no blockers — its own line must not carry a "blocked by" suffix.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "blocker issue") && strings.Contains(line, "blocked by") {
			t.Errorf("#99 line should not have blocker annotation; got: %s", line)
		}
	}
}

// TestPreviewIssues_MixedBatchAnnotatesEachSource verifies that in a batch
// spanning both sources, preview labels each dependent's blocker with the
// source that specific ref was resolved from — a native-relationship ref on
// one issue does not bleed into a body-sourced ref on another.
func TestPreviewIssues_MixedBatchAnnotatesEachSource(t *testing.T) {
	c := baseConfig()
	c.repoSlug = "owner/repo"
	c.label = "ready-for-agent"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "50", Title: "native blocker", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "60", Title: "body blocker", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "10", Title: "native dependent", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "20", Title: "body dependent", Labels: []string{c.label},
		Body: "## Blocked by\n- #60\n"})
	fc.NativeDeps = map[string][]string{"10": {"50"}}

	var buf bytes.Buffer
	if err := previewIssues(c, fc, fc, &buf, nil, t.TempDir(), nil); err != nil {
		t.Fatalf("previewIssues: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "#10  native dependent  (blocked by #50 (native))") {
		t.Errorf("output missing native-sourced annotation for #10; got:\n%s", out)
	}
	if !strings.Contains(out, "#20  body dependent  (blocked by #60 (body))") {
		t.Errorf("output missing body-sourced annotation for #20; got:\n%s", out)
	}
}

// TestPreviewIssues_DepsOfCheckFailure_AnnotatesDistinctly verifies that a
// DepsOf call failure (#752, #1103) is rendered distinctly from both a
// zero-blocker issue and a blocked-by annotation, instead of being silently
// dropped as previewIssues did before this fix threaded result.Failed into
// waves.Input.
func TestPreviewIssues_DepsOfCheckFailure_AnnotatesDistinctly(t *testing.T) {
	c := baseConfig()
	c.repoSlug = "owner/repo"
	c.label = "ready-for-agent"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "12", Title: "deps-of-failed", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "15", Title: "clean", Labels: []string{c.label}})

	it := failDepsOf{Fake: fc, num: "12"}

	var buf bytes.Buffer
	if err := previewIssues(c, it, it, &buf, nil, t.TempDir(), nil); err != nil {
		t.Fatalf("previewIssues: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "#12  deps-of-failed  (blocker check failed; will retry)") {
		t.Errorf("output missing distinct DepsOf-failure annotation for #12; got:\n%s", out)
	}
	if !strings.Contains(out, "#15  clean\n") {
		t.Errorf("output missing plain line for unaffected #15; got:\n%s", out)
	}
}
