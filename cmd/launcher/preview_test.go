package main

import (
	"bytes"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestPreviewIssues_ListsIssuesAndRepo verifies that previewIssues prints the
// candidate issues and target repo without making any mutating Forge calls.
func TestPreviewIssues_ListsIssuesAndRepo(t *testing.T) {
	c := baseConfig()
	c.repoSlug = "owner/repo"
	c.label = "ready-for-agent"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "10", Title: "first issue", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "20", Title: "second issue", Labels: []string{c.label}})

	var buf bytes.Buffer
	if err := previewIssues(c, fc, &buf, nil); err != nil {
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
	if len(fc.SwapCalls) != 0 {
		t.Errorf("previewIssues made %d SwapLabel calls; want 0", len(fc.SwapCalls))
	}
	if len(fc.CommentCalls) != 0 {
		t.Errorf("previewIssues made %d Comment calls; want 0", len(fc.CommentCalls))
	}
}

// TestPreviewIssues_RespectsBarrierFence verifies that issues above the lowest
// fanout-blocker barrier are excluded, matching dispatch behavior.
func TestPreviewIssues_RespectsBarrierFence(t *testing.T) {
	c := baseConfig()
	c.repoSlug = "owner/repo"
	c.label = "ready-for-agent"
	c.barrierLabel = "fanout-blocker"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "5", Title: "before barrier", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "10", Title: "the barrier", Labels: []string{c.barrierLabel, c.label}})
	fc.SetIssue(forge.Issue{Number: "15", Title: "after barrier", Labels: []string{c.label}})

	var buf bytes.Buffer
	if err := previewIssues(c, fc, &buf, nil); err != nil {
		t.Fatalf("previewIssues: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "#5") {
		t.Errorf("output missing #5 (before barrier); got:\n%s", out)
	}
	if !strings.Contains(out, "#10") {
		t.Errorf("output missing #10 (the barrier itself); got:\n%s", out)
	}
	if strings.Contains(out, "#15") {
		t.Errorf("output incorrectly includes #15 (after barrier); got:\n%s", out)
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
	if err := previewIssues(c, fc, &buf, nil); err != nil {
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
