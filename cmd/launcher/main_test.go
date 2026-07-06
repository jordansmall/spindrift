package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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

func TestUnreadyBlockers_CompleteAndClosedAreReady(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "11", State: "OPEN", Labels: []string{"agent-complete"}})
	fc.SetIssue(forge.Issue{Number: "12", State: "CLOSED"})
	c := config{completeLabel: "agent-complete"}
	edges := map[string][]string{"10": {"11", "12"}}
	if got := unreadyBlockers(c, fc, "10", edges); len(got) != 0 {
		t.Errorf("expected no unready blockers, got %v", got)
	}
}

func TestUnreadyBlockers_Mixed(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "11", State: "OPEN", Labels: []string{"agent-complete"}})
	fc.SetIssue(forge.Issue{Number: "12", State: "OPEN"}) // still blocking
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

var errBoxFailed = fmt.Errorf("exit 1")
