package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// TestConfigHasNoModelFields enforces that model/scoutModel/reviewModel were
// removed from the config struct; models forward via BOX_ENV_VARS instead.
func TestConfigHasNoModelFields(t *testing.T) {
	ct := reflect.TypeOf(config{})
	for _, name := range []string{"model", "scoutModel", "reviewModel"} {
		if _, ok := ct.FieldByName(name); ok {
			t.Errorf("config has field %q; remove it — models forward via BOX_ENV_VARS", name)
		}
	}
}

// TestRunnerConfig_DriverMountTargets verifies DRIVER_SKILLS_DIR and
// DRIVER_SESSION_CACHE_DIR (nix-baked from the Driver declaration, ADR 0009)
// reach runner.Config, so the OCI/bwrap adapters mount over the Driver's
// declared paths instead of a hardcoded ".claude" literal (issue #448).
func TestRunnerConfig_DriverMountTargets(t *testing.T) {
	t.Setenv("DRIVER_SKILLS_DIR", "/home/agent/.claude/skills")
	t.Setenv("DRIVER_SESSION_CACHE_DIR", "/home/agent/.claude/projects")

	c := loadConfig()
	rc := runnerConfig(c)

	if rc.DriverSkillsDir != "/home/agent/.claude/skills" {
		t.Errorf("DriverSkillsDir = %q, want /home/agent/.claude/skills", rc.DriverSkillsDir)
	}
	if rc.DriverSessionCacheDir != "/home/agent/.claude/projects" {
		t.Errorf("DriverSessionCacheDir = %q, want /home/agent/.claude/projects", rc.DriverSessionCacheDir)
	}
}

// TestRunnerConfig_DriverSessionCacheDirUnset verifies that an unset
// DRIVER_SESSION_CACHE_DIR (a Driver declaring no session-state dir) reaches
// runner.Config as empty, not a fallback literal.
func TestRunnerConfig_DriverSessionCacheDirUnset(t *testing.T) {
	t.Setenv("DRIVER_SESSION_CACHE_DIR", "")

	c := loadConfig()
	rc := runnerConfig(c)

	if rc.DriverSessionCacheDir != "" {
		t.Errorf("DriverSessionCacheDir = %q, want empty when DRIVER_SESSION_CACHE_DIR is unset", rc.DriverSessionCacheDir)
	}
}

// --- newIssueTracker tests ---

// TestNewIssueTracker_Jira verifies that ISSUE_TRACKER=jira selects a tracker
// backed by the Jira REST API instead of the GitHub gh-exec adapter.
func TestNewIssueTracker_Jira(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"accountId":"abc"}`))
	}))
	defer srv.Close()

	c := minimalValidConfig()
	c.issueTracker = "jira"
	c.jiraBaseURL = srv.URL
	c.jiraProjectKey = "PROJ"
	c.jiraToken = "tok"

	it := newIssueTracker(c)
	slug, err := it.Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if slug != "PROJ" {
		t.Errorf("Probe() = %q, want the Jira adapter (PROJ)", slug)
	}
}

// --- detectCycle tests ---

func TestDetectCycle_Empty(t *testing.T) {
	_, hasCycle := detectCycle(map[string][]string{}, []string{})
	if hasCycle {
		t.Error("expected no cycle in empty graph")
	}
}

func TestDetectCycle_NoCycle_Linear(t *testing.T) {
	// 1 depends on 2, 2 depends on 3 (1→2→3)
	edges := map[string][]string{
		"1": {"2"},
		"2": {"3"},
	}
	node, hasCycle := detectCycle(edges, []string{"1", "2", "3"})
	if hasCycle {
		t.Errorf("expected no cycle, got cycle member %s", node)
	}
}

func TestDetectCycle_NoCycle_Parallel(t *testing.T) {
	// 1 and 2 both depend on 3 (independent blockers)
	edges := map[string][]string{
		"1": {"3"},
		"2": {"3"},
	}
	node, hasCycle := detectCycle(edges, []string{"1", "2", "3"})
	if hasCycle {
		t.Errorf("expected no cycle, got cycle member %s", node)
	}
}

func TestDetectCycle_DirectCycle(t *testing.T) {
	// 1 depends on 2 and 2 depends on 1
	edges := map[string][]string{
		"1": {"2"},
		"2": {"1"},
	}
	_, hasCycle := detectCycle(edges, []string{"1", "2"})
	if !hasCycle {
		t.Error("expected cycle, got none")
	}
}

func TestDetectCycle_TransitiveCycle(t *testing.T) {
	// 1→2→3→1
	edges := map[string][]string{
		"1": {"2"},
		"2": {"3"},
		"3": {"1"},
	}
	_, hasCycle := detectCycle(edges, []string{"1", "2", "3"})
	if !hasCycle {
		t.Error("expected cycle, got none")
	}
}

func TestDetectCycle_ExternalBlockerIgnored(t *testing.T) {
	// 1 depends on 99 (external, not in batch)
	edges := map[string][]string{
		"1": {"99"},
	}
	node, hasCycle := detectCycle(edges, []string{"1"})
	if hasCycle {
		t.Errorf("expected no cycle (external blockers ignored in batch), got cycle member %s", node)
	}
}

// --- unreadyBlockers tests ---

func TestUnreadyBlockers_Pending(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "11", State: "OPEN"}) // no complete label, still open
	c := config{completeLabel: "agent-complete"}
	edges := map[string][]string{"10": {"11"}}
	got := unreadyBlockers(c, fc, "10", edges)
	if !reflect.DeepEqual(got, []string{"11"}) {
		t.Errorf("expected [11], got %v", got)
	}
}

func TestUnreadyBlockers_MergedAndClosedAreReady(t *testing.T) {
	fc := forge.NewFake()
	// #11: PR merged — satisfied by merged PR regardless of labels.
	fc.SetIssue(forge.Issue{Number: "11", State: "OPEN"})
	fc.SetPR("11", forge.PR{URL: "https://github.com/owner/repo/pull/11"})
	fc.SetPRState("https://github.com/owner/repo/pull/11", "MERGED")
	// #12: issue closed with no PR — fallback satisfied.
	fc.SetIssue(forge.Issue{Number: "12", State: "CLOSED"})
	c := config{completeLabel: "agent-complete"}
	edges := map[string][]string{"10": {"11", "12"}}
	if got := unreadyBlockers(c, fc, "10", edges); len(got) != 0 {
		t.Errorf("expected no unready blockers, got %v", got)
	}
}

func TestUnreadyBlockers_Mixed(t *testing.T) {
	fc := forge.NewFake()
	// #11: PR merged — satisfied.
	fc.SetIssue(forge.Issue{Number: "11", State: "OPEN"})
	fc.SetPR("11", forge.PR{URL: "https://github.com/owner/repo/pull/11"})
	fc.SetPRState("https://github.com/owner/repo/pull/11", "MERGED")
	// #12: still open with no merged PR — blocking.
	fc.SetIssue(forge.Issue{Number: "12", State: "OPEN"})
	c := config{completeLabel: "agent-complete"}
	edges := map[string][]string{"10": {"11", "12"}}
	if got := unreadyBlockers(c, fc, "10", edges); !reflect.DeepEqual(got, []string{"12"}) {
		t.Errorf("expected [12], got %v", got)
	}
}

// --- integer-knob parsing tests ---

// TestMaxParallelEdgeCases covers the atoi() fallback for values where zero
// would deadlock the semaphore: 0, negative, and non-numeric all fall back to
// the compiled default (3).
func TestMaxParallelEdgeCases(t *testing.T) {
	t.Cleanup(func() { os.Unsetenv("MAX_PARALLEL") })

	cases := []struct {
		env  string
		want int
	}{
		{"0", 3},   // zero → deadlock guard, must fall back
		{"-1", 3},  // negative → fall back
		{"-99", 3}, // large negative → fall back
		{"abc", 3}, // non-numeric → fall back
		{"", 3},    // unset → fall back to default
		{"1", 1},   // valid positive → use as-is
		{"10", 10}, // larger valid value → use as-is
	}
	for _, tc := range cases {
		os.Setenv("MAX_PARALLEL", tc.env)
		c := loadConfig()
		if c.maxParallel != tc.want {
			t.Errorf("MAX_PARALLEL=%q: got %d, want %d", tc.env, c.maxParallel, tc.want)
		}
	}
}

// TestMaxJobsEdgeCases covers the atoiNonneg() fallback: zero is valid
// (meaning unlimited), negatives fall back to default (0).
func TestMaxJobsEdgeCases(t *testing.T) {
	t.Cleanup(func() { os.Unsetenv("MAX_JOBS") })

	cases := []struct {
		env  string
		want int
	}{
		{"0", 0},   // zero is valid (unlimited)
		{"-1", 0},  // negative → fall back to default
		{"abc", 0}, // non-numeric → fall back to default
		{"", 0},    // unset → fall back to default
		{"5", 5},   // valid positive → use as-is
	}
	for _, tc := range cases {
		os.Setenv("MAX_JOBS", tc.env)
		c := loadConfig()
		if c.maxJobs != tc.want {
			t.Errorf("MAX_JOBS=%q: got %d, want %d", tc.env, c.maxJobs, tc.want)
		}
	}
}

// --- writeBlockedMarker tests ---

func TestWriteBlockedMarker(t *testing.T) {
	pwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(pwd, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeBlockedMarker(pwd, []string{"11", "13"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(pwd, "logs", blockedMarker))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != "#11, #13" {
		t.Errorf("expected %q, got %q", "#11, #13", got)
	}
}

// TestDispatchWaves_FailsDependentWhenBlockerFails verifies that a dependent
// whose in-batch blocker reaches failedLabel is itself failed immediately,
// rather than holding until depsWaitSecs.
func TestDispatchWaves_FailsDependentWhenBlockerFails(t *testing.T) {
	c := baseConfig()
	c.label = "agent-trigger"
	c.maxParallel = 2
	c.branchPrefix = "agent/issue-"
	c.mergePollInterval = 0
	c.mergePollTimeout = 0
	c.depsPollSecs = 1
	c.depsWaitSecs = 2

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.label}})

	fr := runner.NewFake()
	fr.RunErr = errBoxFailed

	edges := map[string][]string{"2": {"1"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(c, fc)
	if err := dispatchWaves(c, fc, f, s, []issue{
		{number: "1", title: "blocker"},
		{number: "2", title: "dependent"},
	}, edges); err != nil {
		t.Fatalf("dispatchWaves: %v", err)
	}

	iss2, err := fc.Issue("2")
	if err != nil {
		t.Fatalf("Issue(2): %v", err)
	}
	if !containsLabel(iss2.Labels, c.failedLabel) {
		t.Errorf("issue 2 must have %q when blocker failed; labels=%v", c.failedLabel, iss2.Labels)
	}
}

// TestValidate_RepoSlugRequired verifies that validate() fails when REPO_SLUG
// is empty, confirming the required-validation contract is not masked by any
// settings-baked preamble default (which bakes an empty ${REPO_SLUG:-}).
func TestValidate_RepoSlugRequired(t *testing.T) {
	c := minimalValidConfig()
	c.repoSlug = ""
	err := validate(c)
	if err == nil {
		t.Fatal("validate() must require REPO_SLUG when empty")
	}
	if !strings.Contains(err.Error(), "REPO_SLUG") {
		t.Errorf("error should mention REPO_SLUG, got: %v", err)
	}
}

// TestValidateMergeMode_RejectsUnknown verifies that validate() fails fast when
// MERGE_MODE is set to an unrecognised value.
func TestValidateMergeMode_RejectsUnknown(t *testing.T) {
	c := minimalValidConfig()
	c.mergeMode = "turbo"
	if err := validate(c); err == nil {
		t.Fatal("validate() should reject unrecognised MERGE_MODE")
	}
}

// TestValidate_JiraRequiresBaseURLProjectKeyToken verifies validate() fails
// fast when ISSUE_TRACKER=jira but the Jira connection fields are missing,
// rather than deferring to a runtime Jira API error.
func TestValidate_JiraRequiresBaseURLProjectKeyToken(t *testing.T) {
	base := minimalValidConfig()
	base.issueTracker = "jira"
	base.jiraBaseURL = "https://example.atlassian.net"
	base.jiraProjectKey = "PROJ"
	base.jiraToken = "tok"

	if err := validate(base); err != nil {
		t.Fatalf("fully configured jira config should validate: %v", err)
	}

	for _, field := range []string{"jiraBaseURL", "jiraProjectKey", "jiraToken"} {
		c := base
		switch field {
		case "jiraBaseURL":
			c.jiraBaseURL = ""
		case "jiraProjectKey":
			c.jiraProjectKey = ""
		case "jiraToken":
			c.jiraToken = ""
		}
		if err := validate(c); err == nil {
			t.Errorf("validate() must require %s when ISSUE_TRACKER=jira", field)
		}
	}
}

// TestValidate_JiraFieldsOptionalForGitHub verifies validate() does not
// require Jira fields when ISSUE_TRACKER is unset/github.
func TestValidate_JiraFieldsOptionalForGitHub(t *testing.T) {
	c := minimalValidConfig()
	if err := validate(c); err != nil {
		t.Fatalf("github default must not require jira fields: %v", err)
	}
}

// TestValidateMergeMode_AcceptsKnown verifies that validate() accepts the three
// documented MERGE_MODE values.
func TestValidateMergeMode_AcceptsKnown(t *testing.T) {
	for _, mode := range []string{"immediate", "auto", "manual"} {
		c := minimalValidConfig()
		c.mergeMode = mode
		if err := validate(c); err != nil {
			t.Errorf("validate() rejected valid MERGE_MODE %q: %v", mode, err)
		}
	}
}

// TestValidateOverlapGate_RejectsUnknown verifies that validate() fails fast
// when OVERLAP_GATE is set to an unrecognised value.
func TestValidateOverlapGate_RejectsUnknown(t *testing.T) {
	c := minimalValidConfig()
	c.overlapGate = "yolo"
	if err := validate(c); err == nil {
		t.Fatal("validate() should reject unrecognised OVERLAP_GATE")
	}
}

// TestValidateOverlapGate_AcceptsKnown verifies that validate() accepts the
// two documented OVERLAP_GATE values.
func TestValidateOverlapGate_AcceptsKnown(t *testing.T) {
	for _, mode := range []string{"defer", "off"} {
		c := minimalValidConfig()
		c.overlapGate = mode
		if err := validate(c); err != nil {
			t.Errorf("validate() rejected valid OVERLAP_GATE %q: %v", mode, err)
		}
	}
}

// TestValidateDriver_RejectsUnknown verifies that validate() fails fast when
// DRIVER is set to a name absent from the Go Driver registry (ADR 0009).
func TestValidateDriver_RejectsUnknown(t *testing.T) {
	c := minimalValidConfig()
	c.driver = "bogus"
	if err := validate(c); err == nil {
		t.Fatal("validate() should reject unrecognised DRIVER")
	}
}

// TestValidateDriver_AcceptsKnownAndEmpty verifies that validate() accepts
// the registered "claude" Driver as well as an empty DRIVER (which defaults
// to "claude").
func TestValidateDriver_AcceptsKnownAndEmpty(t *testing.T) {
	for _, d := range []string{"claude", ""} {
		c := minimalValidConfig()
		c.driver = d
		if err := validate(c); err != nil {
			t.Errorf("validate() rejected valid DRIVER %q: %v", d, err)
		}
	}
}

// TestValidateIssueTracker_RejectsUnknown verifies that validate() fails fast
// when ISSUE_TRACKER is set to an unrecognised value.
func TestValidateIssueTracker_RejectsUnknown(t *testing.T) {
	c := minimalValidConfig()
	c.issueTracker = "jira"
	if err := validate(c); err == nil {
		t.Fatal("validate() should reject unrecognised ISSUE_TRACKER")
	}
}

// TestValidateIssueTracker_AcceptsKnown verifies that validate() accepts the
// two documented ISSUE_TRACKER values.
func TestValidateIssueTracker_AcceptsKnown(t *testing.T) {
	for _, tracker := range []string{"github", "local"} {
		c := minimalValidConfig()
		c.issueTracker = tracker
		if err := validate(c); err != nil {
			t.Errorf("validate() rejected valid ISSUE_TRACKER %q: %v", tracker, err)
		}
	}
}

// TestNewIssueTracker_Local verifies that ISSUE_TRACKER=local selects a
// tracker reading from localIssuesDir instead of the GitHub gh-exec adapter.
func TestNewIssueTracker_Local(t *testing.T) {
	dir := t.TempDir()
	issueFile := `---
title: Fix the thing
state: ready-for-agent
labels: []
created: 2026-07-09T12:00:00Z
---
body
`
	if err := os.WriteFile(filepath.Join(dir, "fix-thing.md"), []byte(issueFile), 0o644); err != nil {
		t.Fatal(err)
	}

	c := minimalValidConfig()
	c.issueTracker = "local"
	c.localIssuesDir = dir
	c.label = "ready-for-agent"

	it := newIssueTracker(c)
	issues, err := it.ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].Number != "fix-thing" {
		t.Errorf("ListIssues = %+v, want [fix-thing]", issues)
	}
}

// TestValidateCodeForge_RejectsUnknown verifies that validate() fails fast when
// CODE_FORGE is set to an unrecognised value.
func TestValidateCodeForge_RejectsUnknown(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "gitlab"
	if err := validate(c); err == nil {
		t.Fatal("validate() should reject unrecognised CODE_FORGE")
	}
}

// TestValidateCodeForge_Git_RequiresRemoteURL verifies that validate() fails
// fast when CODE_FORGE=git but no remote URL is configured — the git Code
// Forge has nothing to clone from or push to without one.
func TestValidateCodeForge_Git_RequiresRemoteURL(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "git"
	c.codeForgeRemoteURL = ""
	err := validate(c)
	if err == nil {
		t.Fatal("validate() should require CODE_FORGE_REMOTE_URL when CODE_FORGE=git")
	}
	if !strings.Contains(err.Error(), "CODE_FORGE_REMOTE_URL") {
		t.Errorf("error should mention CODE_FORGE_REMOTE_URL, got: %v", err)
	}
}

// TestValidateCodeForge_AcceptsKnown verifies that validate() accepts both
// documented CODE_FORGE values.
func TestValidateCodeForge_AcceptsKnown(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "github"
	if err := validate(c); err != nil {
		t.Errorf("validate() rejected CODE_FORGE=github: %v", err)
	}

	c = minimalValidConfig()
	c.codeForge = "git"
	c.codeForgeRemoteURL = "https://git.example.com/owner/repo.git"
	if err := validate(c); err != nil {
		t.Errorf("validate() rejected valid CODE_FORGE=git config: %v", err)
	}
}

// TestNewCodeForge_Git_ReturnsPushOnlyAdapter verifies that CODE_FORGE=git
// wires newCodeForge to the push-only git adapter (no PR/CI/auto-merge
// concept) instead of the github gh-exec adapter.
func TestNewCodeForge_Git_ReturnsPushOnlyAdapter(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "git"
	c.codeForgeRemoteURL = "https://git.example.com/owner/repo.git"

	cf := newCodeForge(c)

	if ok, err := cf.CanAutoMerge(); err != nil || ok {
		t.Errorf("CanAutoMerge = (%v, %v), want (false, nil) for the git Code Forge", ok, err)
	}
	if _, found, err := cf.PRForBranch("agent/issue-1"); err != nil || found {
		t.Errorf("PRForBranch = (_, %v, %v), want (_, false, nil) for the git Code Forge", found, err)
	}
}

// minimalValidConfig returns a config that passes validate() so tests can
// mutate exactly one field at a time.
func minimalValidConfig() config {
	return config{
		repoSlug:         "owner/repo",
		gitUserName:      "bot",
		gitUserEmail:     "bot@example.com",
		ghToken:          "ghp_test",
		claudeOAuthToken: "tok",
		runtime:          "echo", // echo is always on PATH
		mergeMode:        "manual",
		issueTracker:     "github",
		codeForge:        "github",
		overlapGate:      "defer",
	}
}

var errBoxFailed = fmt.Errorf("exit 1")

// --- runDoctor tests ---

func TestDoctor_Success(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	var buf bytes.Buffer
	if err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "owner/repo") {
		t.Errorf("want output to contain resolved repo, got %q", buf.String())
	}
}

// TestDoctor_ReportsEachSeamsOwnSlug verifies runDoctor prints each seam's own
// Probe() result — not the IssueTracker's slug reused for the CodeForge line
// — since under ISSUE_TRACKER=jira the two seams resolve to different
// identities (a Jira project key vs a GitHub repo slug).
func TestDoctor_ReportsEachSeamsOwnSlug(t *testing.T) {
	it := forge.NewFake()
	it.ProbeRepo = "PROJ"
	it.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	cf := forge.NewFake()
	cf.ProbeRepo = "owner/repo"

	var buf bytes.Buffer
	if err := runDoctor(it, cf, defaultLabelConfig(), &buf, strings.NewReader(""), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "issue tracker confirmed — PROJ") {
		t.Errorf("want issue tracker line to report PROJ, got %q", out)
	}
	if !strings.Contains(out, "code forge confirmed — owner/repo") {
		t.Errorf("want code forge line to report owner/repo, got %q", out)
	}
}

func TestDoctor_AuthFailure(t *testing.T) {
	f := forge.NewFake()
	f.ProbeErr = forge.ErrAuthFailure

	var buf bytes.Buffer
	err := runDoctor(f, f, config{}, &buf, strings.NewReader(""), false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, forge.ErrAuthFailure) {
		t.Errorf("want ErrAuthFailure, got %v", err)
	}
}

// TestDoctor_AuthFailure_Jira verifies the auth-failure remediation text
// names JIRA_TOKEN, not GH_TOKEN, when the issue tracker is jira — the
// generic message would misdirect an operator debugging a Jira probe.
func TestDoctor_AuthFailure_Jira(t *testing.T) {
	f := forge.NewFake()
	f.ProbeErr = forge.ErrAuthFailure

	var buf bytes.Buffer
	err := runDoctor(f, f, config{issueTracker: "jira"}, &buf, strings.NewReader(""), false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "JIRA_TOKEN") {
		t.Errorf("want error to mention JIRA_TOKEN, got: %v", err)
	}
}

func TestDoctor_RepoNotFound(t *testing.T) {
	f := forge.NewFake()
	f.ProbeErr = forge.ErrRepoNotFound

	var buf bytes.Buffer
	err := runDoctor(f, f, config{}, &buf, strings.NewReader(""), false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, forge.ErrRepoNotFound) {
		t.Errorf("want ErrRepoNotFound, got %v", err)
	}
}

func defaultLabelConfig() config {
	return config{
		label:           "ready-for-agent",
		inProgressLabel: "agent-in-progress",
		failedLabel:     "agent-failed",
		completeLabel:   "agent-complete",
	}
}

func TestDoctor_LabelsAllPresent(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	var buf bytes.Buffer
	if err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, label := range []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"} {
		if !strings.Contains(out, label) {
			t.Errorf("want output to contain label %q, got:\n%s", label, out)
		}
	}
	if !strings.Contains(out, "present") {
		t.Errorf("want output to mention 'present', got:\n%s", out)
	}
}

func TestDoctor_LabelsSomeMissing(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress"}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false)
	if err == nil {
		t.Fatal("expected non-zero exit for missing labels, got nil")
	}
	out := buf.String()
	if !strings.Contains(out, "missing") {
		t.Errorf("want output to mention 'missing', got:\n%s", out)
	}
}

func TestDoctor_LabelsAllMissing(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false)
	if err == nil {
		t.Fatal("expected non-zero exit for all-missing labels, got nil")
	}
	out := buf.String()
	if !strings.Contains(out, "missing") {
		t.Errorf("want output to mention 'missing', got:\n%s", out)
	}
}

func TestDoctor_NoTTY_NoPrompt(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent"} // three missing

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false)
	if err == nil {
		t.Fatal("expected non-zero exit for missing labels, got nil")
	}
	if strings.Contains(buf.String(), "[y/N]") {
		t.Errorf("no-TTY path must not show a prompt, got:\n%s", buf.String())
	}
	if len(f.CreateLabelCalls) != 0 {
		t.Errorf("no-TTY path must not create labels, got %d calls", len(f.CreateLabelCalls))
	}
}

func TestDoctor_TTY_Decline(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent"} // three missing

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader("n\n"), true)
	if err == nil {
		t.Fatal("expected non-zero exit on decline, got nil")
	}
	if !strings.Contains(buf.String(), "[y/N]") {
		t.Errorf("TTY path must show the prompt, got:\n%s", buf.String())
	}
	if len(f.CreateLabelCalls) != 0 {
		t.Errorf("decline must not create labels, got %d calls", len(f.CreateLabelCalls))
	}
}

func TestDoctor_TTY_Confirm(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	// Two labels missing: agent-failed and agent-complete
	f.Labels = []string{"ready-for-agent", "agent-in-progress"}
	// After creation the fake doesn't auto-add to Labels, so script the
	// second ListLabels call (re-verify) to return all four.
	f.LabelsSeq = [][]string{
		{"ready-for-agent", "agent-in-progress"},                                   // first check
		{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}, // re-verify
	}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader("y\n"), true)
	if err != nil {
		t.Fatalf("unexpected error after confirm: %v", err)
	}
	if len(f.CreateLabelCalls) != 2 {
		t.Fatalf("want 2 CreateLabel calls, got %d", len(f.CreateLabelCalls))
	}
	names := []string{f.CreateLabelCalls[0].Name, f.CreateLabelCalls[1].Name}
	if !contains(names, "agent-failed") || !contains(names, "agent-complete") {
		t.Errorf("want agent-failed and agent-complete created, got %v", names)
	}
	// Verify default colors are from triageLabelMeta
	for _, call := range f.CreateLabelCalls {
		if call.Color == "" || call.Color == "ededed" {
			t.Errorf("label %q should use a named color, got %q", call.Name, call.Color)
		}
	}
	out := buf.String()
	if !strings.Contains(out, "ok: all triage labels present") {
		t.Errorf("want success message after creation, got:\n%s", out)
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// TestEngageAliasRemoved asserts that the deprecated `engage` subcommand
// handler has been deleted from main.go. The handler was removed in v0.2.0;
// this test prevents accidental re-introduction.
func TestEngageAliasRemoved(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	if strings.Contains(string(data), `args[0] == "engage"`) {
		t.Error(`main.go still dispatches the deprecated "engage" subcommand; remove the handler`)
	}
}
