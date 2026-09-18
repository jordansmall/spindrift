package main

import (
	"bytes"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/waves"
)

// Preview is read-only, so it must make no mutating Forge calls.
func TestPreviewIssues_ListsIssuesAndRepo(t *testing.T) {
	c := baseConfig()
	c.repoSlug = "owner/repo"
	c.label = "ready-for-agent"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "10", Title: "first issue", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "20", Title: "second issue", Labels: []string{c.label}})

	var buf bytes.Buffer
	if err := previewIssues(c, fc, fc, capsFor(fc, fc), &buf, nil, t.TempDir(), nil); err != nil {
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

// The operator needs to see which merge mode is armed before dispatching.
func TestPreviewIssues_PrintsMergeMode(t *testing.T) {
	c := baseConfig()
	c.repoSlug = "owner/repo"
	c.label = "ready-for-agent"
	c.mergeMode = "immediate"
	fc := forge.NewFake()

	var buf bytes.Buffer
	if err := previewIssues(c, fc, fc, capsFor(fc, fc), &buf, nil, t.TempDir(), nil); err != nil {
		t.Fatalf("previewIssues: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "immediate") {
		t.Errorf("previewIssues output must include merge mode; got:\n%s", out)
	}
}

// A fully-local run has no repo slug, and the banner used to print a bare
// "repo: " line for it (issue #1895).
func TestPreviewIssues_FullyLocal_OmitsBareRepoLine(t *testing.T) {
	c := baseConfig()
	c.repoSlug = ""
	c.issueTracker = "local"
	c.label = "ready-for-agent"
	c.mergeMode = "immediate"
	fc := forge.NewFake()

	var buf bytes.Buffer
	if err := previewIssues(c, fc, fc, capsFor(fc, fc), &buf, nil, t.TempDir(), nil); err != nil {
		t.Fatalf("previewIssues: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "repo: ") {
		t.Errorf("previewIssues output must not print a bare 'repo: ' with an empty slug; got:\n%s", out)
	}
	if !strings.Contains(out, "immediate") {
		t.Errorf("previewIssues output must still include merge mode; got:\n%s", out)
	}
}

// printPlan is the one blocker-annotation printer shared by the
// discovered-batch and selective preview paths.
func TestPrintPlan_AnnotatesBlockers(t *testing.T) {
	plan := waves.Plan{
		Batch: waves.Batch{
			Issues: []waves.Issue{
				{Number: "99", Title: "blocker issue"},
				{Number: "15", Title: "dependent"},
			},
			Edges:   map[string][]string{"15": {"99"}},
			Sources: waves.Sources{"15": {"99": forge.DepSourceNative}},
		},
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

func TestPrintHelp_ShowsPreview(t *testing.T) {
	var buf bytes.Buffer
	printHelp(&buf)
	if !strings.Contains(buf.String(), "preview") {
		t.Error("help output missing 'preview' subcommand")
	}
}

func TestPreviewIssues_EmptyQueue(t *testing.T) {
	c := baseConfig()
	c.repoSlug = "owner/repo"
	c.label = "ready-for-agent"
	fc := forge.NewFake()

	var buf bytes.Buffer
	if err := previewIssues(c, fc, fc, capsFor(fc, fc), &buf, nil, t.TempDir(), nil); err != nil {
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

// probe.go no longer special-cases bwrap before the fetch step, so a bwrap and
// an OCI runnerKind reach the same not-a-git-repository path when pwd is not a
// checkout. c.runtime stays set to "podman" here, but nothing in this path
// reads it; internal/freshness's own tests cover that.
func TestPreviewIssues_PrintsFreshnessLine(t *testing.T) {
	c := baseConfig()
	c.repoSlug = "owner/repo"
	c.label = "ready-for-agent"
	c.runtime = "podman"
	c.runnerKind = "bwrap"
	fc := forge.NewFake()

	var buf bytes.Buffer
	if err := previewIssues(c, fc, fc, capsFor(fc, fc), &buf, nil, t.TempDir(), nil); err != nil {
		t.Fatalf("previewIssues: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "freshness:") {
		t.Errorf("output missing freshness line; got:\n%s", out)
	}
	if !strings.Contains(out, "is not a git repository") {
		t.Errorf("output missing the not-a-git-repository not-applicable message; got:\n%s", out)
	}
}

// Preview must print a real fresh/stale verdict for bwrap, not just the
// not-applicable path TestPreviewIssues_PrintsFreshnessLine covers (issue #2667
// AC3). The fixture needs a real git clone with an origin remote and an
// injected Evaluator to drive Probe all the way to an outPath comparison.
func TestPreviewIssues_Bwrap_PrintsRealFreshnessLine(t *testing.T) {
	const staleHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const freshHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	c := baseConfig()
	c.repoSlug = "owner/repo"
	c.label = "ready-for-agent"
	c.runnerKind = "bwrap"
	c.baseBranch = "main"
	c.flakeImageAttr = ".#packages.x86_64-linux.agent-closure"
	c.imageTag = "/nix/store/" + staleHash + "-agent-closure"
	fc := forge.NewFake()

	pwd := newConsoleGitRepo(t, "main")
	eval := &freshness.Fake{OutPath: "/nix/store/" + freshHash + "-agent-closure"}

	var buf bytes.Buffer
	if err := previewIssues(c, fc, fc, capsFor(fc, fc), &buf, nil, pwd, eval); err != nil {
		t.Fatalf("previewIssues: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "rebuild needed") {
		t.Errorf("output missing the rebuild-needed verdict for a diverging bwrap closure; got:\n%s", out)
	}
	if !strings.Contains(out, "loaded closure") {
		t.Errorf("output does not name the loaded closure; got:\n%s", out)
	}
}

// The launcher-currency line prints FLAKE_LAUNCHER_ATTR unconditionally from
// config (issue #2677). It is not a freshness verdict, despite sitting next to
// the freshness line.
func TestPreviewIssues_PrintsLauncherCurrencyLine(t *testing.T) {
	c := baseConfig()
	c.repoSlug = "owner/repo"
	c.label = "ready-for-agent"
	c.flakeLauncherAttr = ".#packages.x86_64-linux.launcher-currency"
	fc := forge.NewFake()

	var buf bytes.Buffer
	if err := previewIssues(c, fc, fc, capsFor(fc, fc), &buf, nil, t.TempDir(), nil); err != nil {
		t.Fatalf("previewIssues: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "launcher-currency-attr:") {
		t.Errorf("output missing launcher-currency-attr line; got:\n%s", out)
	}
	if !strings.Contains(out, ".#packages.x86_64-linux.launcher-currency") {
		t.Errorf("output missing launcher-currency attribute value; got:\n%s", out)
	}
}

// FLAKE_LAUNCHER_ATTR is empty in the unwrapped `go run` case, and the line
// then ended in a bare, valueless colon (issue #2677 review finding). The
// placeholder matches the adjacent freshness line, which always carries a
// non-empty res.Message.
func TestPreviewIssues_PrintsLauncherCurrencyPlaceholderWhenUnset(t *testing.T) {
	c := baseConfig()
	c.repoSlug = "owner/repo"
	c.label = "ready-for-agent"
	c.flakeLauncherAttr = ""
	fc := forge.NewFake()

	var buf bytes.Buffer
	if err := previewIssues(c, fc, fc, capsFor(fc, fc), &buf, nil, t.TempDir(), nil); err != nil {
		t.Fatalf("previewIssues: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "launcher-currency-attr: \n") {
		t.Errorf("launcher-currency-attr line printed bare with no value; got:\n%s", out)
	}
	if !strings.Contains(out, "launcher-currency-attr: (unset)\n") {
		t.Errorf("output missing launcher-currency-attr placeholder for unset attr; got:\n%s", out)
	}
}

// Bare preview here means preview with no positional issue numbers.
func TestPreviewIssues_BareAnnotatesBlockers(t *testing.T) {
	c := baseConfig()
	c.repoSlug = "owner/repo"
	c.label = "ready-for-agent"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "99", Title: "blocker issue", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "15", Title: "dependent", Labels: []string{c.label},
		Body: "## Blocked by\n- #99\n"})

	var buf bytes.Buffer
	if err := previewIssues(c, fc, fc, capsFor(fc, fc), &buf, nil, t.TempDir(), nil); err != nil {
		t.Fatalf("previewIssues: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "#15") {
		t.Errorf("output missing #15; got:\n%s", out)
	}
	// The ref comes from #15's "## Blocked by" body section, not NativeDeps, so
	// the annotation must name the body source.
	if !strings.Contains(out, "blocked by #99 (body)") {
		t.Errorf("output missing body-sourced blocker annotation for #15; got:\n%s", out)
	}
	// The #99 line is located by its "blocker issue" title. #99 has no blockers,
	// so that line must carry no "blocked by" suffix.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "blocker issue") && strings.Contains(line, "blocked by") {
			t.Errorf("#99 line should not have blocker annotation; got: %s", line)
		}
	}
}

// Each dependent's blocker carries the source that specific ref resolved from,
// so a native ref on one issue must not bleed into a body-sourced ref on
// another.
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
	if err := previewIssues(c, fc, fc, capsFor(fc, fc), &buf, nil, t.TempDir(), nil); err != nil {
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

// previewIssues silently dropped DepsOf call failures (#752, #1103) until the
// fix threaded result.Failed into waves.Input. The failure must read distinctly
// from both a zero-blocker issue and a blocked-by annotation.
func TestPreviewIssues_DepsOfCheckFailure_AnnotatesDistinctly(t *testing.T) {
	c := baseConfig()
	c.repoSlug = "owner/repo"
	c.label = "ready-for-agent"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "12", Title: "deps-of-failed", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "15", Title: "clean", Labels: []string{c.label}})

	it := failDepsOf{Fake: fc, num: "12"}

	var buf bytes.Buffer
	if err := previewIssues(c, it, it, capsFor(it, it), &buf, nil, t.TempDir(), nil); err != nil {
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
