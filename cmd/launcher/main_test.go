package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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

// --- parseBlockerRefs tests ---

func TestParseBlockerRefs_Empty(t *testing.T) {
	refs := parseBlockerRefs("")
	if len(refs) != 0 {
		t.Errorf("expected [], got %v", refs)
	}
}

func TestParseBlockerRefs_NoRefs(t *testing.T) {
	refs := parseBlockerRefs("This is just a regular issue body with no blockers.")
	if len(refs) != 0 {
		t.Errorf("expected [], got %v", refs)
	}
}

func TestParseBlockerRefs_DependsOn(t *testing.T) {
	refs := parseBlockerRefs("This issue depends on #12 to work correctly.")
	if len(refs) != 1 || refs[0] != "12" {
		t.Errorf("expected [12], got %v", refs)
	}
}

func TestParseBlockerRefs_BlockedBy(t *testing.T) {
	refs := parseBlockerRefs("blocked by #1")
	if len(refs) != 1 || refs[0] != "1" {
		t.Errorf("expected [1], got %v", refs)
	}
}

func TestParseBlockerRefs_CaseInsensitive(t *testing.T) {
	refs := parseBlockerRefs("DEPENDS ON #5")
	if len(refs) != 1 || refs[0] != "5" {
		t.Errorf("expected [5], got %v", refs)
	}

	refs2 := parseBlockerRefs("Blocked By #7")
	if len(refs2) != 1 || refs2[0] != "7" {
		t.Errorf("expected [7], got %v", refs2)
	}
}

// The old bash regex only caught the first ref per line — Go must catch all.
func TestParseBlockerRefs_MultipleRefsOnOneLine(t *testing.T) {
	refs := parseBlockerRefs("blocked by #12 and #13")
	sort.Strings(refs)
	if len(refs) != 2 || refs[0] != "12" || refs[1] != "13" {
		t.Errorf("expected [12, 13], got %v", refs)
	}
}

func TestParseBlockerRefs_HeaderListFormat(t *testing.T) {
	body := "## Blocked by\n- #56 (some issue)\n- #57"
	refs := parseBlockerRefs(body)
	sort.Strings(refs)
	if len(refs) != 2 || refs[0] != "56" || refs[1] != "57" {
		t.Errorf("expected [56, 57], got %v", refs)
	}
}

func TestParseBlockerRefs_HeaderListWithColon(t *testing.T) {
	body := "## Blocked by:\n- #3\n- #4"
	refs := parseBlockerRefs(body)
	sort.Strings(refs)
	if len(refs) != 2 || refs[0] != "3" || refs[1] != "4" {
		t.Errorf("expected [3, 4], got %v", refs)
	}
}

func TestParseBlockerRefs_HeaderSectionEndsOnNextHeading(t *testing.T) {
	body := "## Blocked by\n- #1\n## Other section\n- #2"
	refs := parseBlockerRefs(body)
	if len(refs) != 1 || refs[0] != "1" {
		t.Errorf("expected [1], got %v", refs)
	}
}

func TestParseBlockerRefs_Deduplication(t *testing.T) {
	// Same ref appears in both inline and header-list format.
	body := "depends on #5\n## Blocked by\n- #5"
	refs := parseBlockerRefs(body)
	if len(refs) != 1 || refs[0] != "5" {
		t.Errorf("expected [5] (deduplicated), got %v", refs)
	}
}

func TestParseBlockerRefs_ListItemMultipleRefs(t *testing.T) {
	// A single list item can name multiple issues: "- #56 and #57"
	body := "## Blocked by\n- #56 and #57"
	refs := parseBlockerRefs(body)
	sort.Strings(refs)
	if len(refs) != 2 || refs[0] != "56" || refs[1] != "57" {
		t.Errorf("expected [56, 57], got %v", refs)
	}
}

// TestParseBlockerRefs_CommaSeparated ensures comma-separated refs are
// still collected when there is no "and".
func TestParseBlockerRefs_CommaSeparated(t *testing.T) {
	refs := parseBlockerRefs("depends on #12, #13")
	sort.Strings(refs)
	if len(refs) != 2 || refs[0] != "12" || refs[1] != "13" {
		t.Errorf("expected [12, 13], got %v", refs)
	}
}

// TestParseBlockerRefs_SlashSeparated ensures slash-separated refs work and
// prose following them is not captured.
func TestParseBlockerRefs_SlashSeparated(t *testing.T) {
	refs := parseBlockerRefs("depends on #1 / #2 but not #3")
	sort.Strings(refs)
	if len(refs) != 2 || refs[0] != "1" || refs[1] != "2" {
		t.Errorf("expected [1, 2], got %v", refs)
	}
}

// TestParseBlockerRefs_InlineStopsAtProse checks that refs in prose after the
// ref list are not captured as blockers.
func TestParseBlockerRefs_InlineStopsAtProse(t *testing.T) {
	refs := parseBlockerRefs("This depends on #12. See also the discussion in #99.")
	if len(refs) != 1 || refs[0] != "12" {
		t.Errorf("expected [12], got %v", refs)
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
	if err := dispatchWaves(c, fc, dir, fr, []issue{
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

// TestValidateMergeMode_RejectsUnknown verifies that validate() fails fast when
// MERGE_MODE is set to an unrecognised value.
func TestValidateMergeMode_RejectsUnknown(t *testing.T) {
	c := minimalValidConfig()
	c.mergeMode = "turbo"
	if err := validate(c); err == nil {
		t.Fatal("validate() should reject unrecognised MERGE_MODE")
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
	}
}

var errBoxFailed = fmt.Errorf("exit 1")

// --- runDoctor tests ---

func TestDoctor_Success(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	var buf bytes.Buffer
	if err := runDoctor(f, defaultLabelConfig(), &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "owner/repo") {
		t.Errorf("want output to contain resolved repo, got %q", buf.String())
	}
}

func TestDoctor_AuthFailure(t *testing.T) {
	f := forge.NewFake()
	f.ProbeErr = forge.ErrAuthFailure

	var buf bytes.Buffer
	err := runDoctor(f, config{}, &buf)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, forge.ErrAuthFailure) {
		t.Errorf("want ErrAuthFailure, got %v", err)
	}
}

func TestDoctor_RepoNotFound(t *testing.T) {
	f := forge.NewFake()
	f.ProbeErr = forge.ErrRepoNotFound

	var buf bytes.Buffer
	err := runDoctor(f, config{}, &buf)
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
	if err := runDoctor(f, defaultLabelConfig(), &buf); err != nil {
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
	err := runDoctor(f, defaultLabelConfig(), &buf)
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
	err := runDoctor(f, defaultLabelConfig(), &buf)
	if err == nil {
		t.Fatal("expected non-zero exit for all-missing labels, got nil")
	}
	out := buf.String()
	if !strings.Contains(out, "missing") {
		t.Errorf("want output to mention 'missing', got:\n%s", out)
	}
}
