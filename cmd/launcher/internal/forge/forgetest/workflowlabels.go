package forgetest

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// ParseWorkflowRemoveLabelSet reads the workflow YAML file at path and
// returns the label set from the first key: line found (e.g.
// "claim-remove-labels:" or "remove-labels:"), plus the raw matched value
// for callers that want it in an error message. It fails the test via
// t.Fatalf if the file can't be read or the key isn't found — shared by
// claim_strip_parity_test.go (package forge) and exec_test.go (package
// github), which both need the same "labels a workflow's remove-label(s)
// line lists" extraction (#2507).
func ParseWorkflowRemoveLabelSet(t *testing.T, path, key string) (map[string]bool, string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	line := regexp.MustCompile(regexp.QuoteMeta(key) + `:\s*(\S.*)`)
	m := line.FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatalf("%s: no %q: line found", path, key)
	}
	set := map[string]bool{}
	for _, l := range strings.Fields(m[1]) {
		set[l] = true
	}
	return set, m[1]
}
