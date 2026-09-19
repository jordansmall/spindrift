package forgetest

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// ParseWorkflowRemoveLabelSet returns the label set on the first "<key>:" line
// of the workflow YAML at path, plus the raw matched value. It calls t.Fatalf
// when the file cannot be read or the key is missing (#2507).
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
