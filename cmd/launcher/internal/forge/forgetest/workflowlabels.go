package forgetest

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var blockScalarIndicator = regexp.MustCompile(`^[>|][-+]?$`)

// ParseWorkflowRemoveLabelSet returns the label set on the first "<key>:" line
// of the workflow YAML at path, plus the raw matched value. A folded or literal
// block scalar (">-", "|") yields its continuation lines: those indented deeper
// than the key, up to the first blank or shallower line. It calls t.Fatalf when
// the file cannot be read or the key is missing (#2507).
func ParseWorkflowRemoveLabelSet(t *testing.T, path, key string) (map[string]bool, string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(raw)
	keyLine := regexp.MustCompile(`(?m)^([ \t]*)` + regexp.QuoteMeta(key) + `:[ \t]*(\S.*)?$`)
	m := keyLine.FindStringSubmatchIndex(text)
	if m == nil {
		t.Fatalf("%s: no %q: line found", path, key)
	}
	keyIndent := m[3] - m[2]
	value := ""
	if m[4] >= 0 {
		value = strings.TrimSpace(text[m[4]:m[5]])
	}
	if blockScalarIndicator.MatchString(value) {
		var lines []string
		// Skip the remainder of the key line itself.
		for _, l := range strings.Split(text[m[1]:], "\n")[1:] {
			if strings.TrimSpace(l) == "" || len(l)-len(strings.TrimLeft(l, " \t")) <= keyIndent {
				break
			}
			lines = append(lines, l)
		}
		value = strings.Join(lines, "\n")
	}
	set := map[string]bool{}
	for _, l := range strings.Fields(value) {
		set[l] = true
	}
	return set, value
}
