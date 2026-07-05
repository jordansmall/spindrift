package main

import (
	"reflect"
	"sort"
	"testing"
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
