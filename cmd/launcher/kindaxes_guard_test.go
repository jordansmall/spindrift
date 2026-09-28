package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// kindAxisForbidden pins one old kind-shaped comparison that the descriptor
// axes (Contract, FilerRelayGate, Tracker, Keying, FindingLabel) replaced
// (issue #3989). why names the axis a caller should read instead; a line
// that also matches unless is a legitimate use of the same spelling.
type kindAxisForbidden struct {
	pattern *regexp.Regexp
	unless  *regexp.Regexp
	why     string
}

func (fp kindAxisForbidden) matches(line string) bool {
	return fp.pattern.MatchString(line) && (fp.unless == nil || !fp.unless.MatchString(line))
}

const settleWhy = "compares the settle strategy -- read Contract, FilerRelayGate, or Tracker instead"

var kindAxisForbiddenPatterns = []kindAxisForbidden{
	{pattern: regexp.MustCompile(`\.Settle\s*[!=]=\s*dispatchkind\.Settle`), why: settleWhy},
	{pattern: regexp.MustCompile(`dispatchkind\.Settle(Merge|Verdict|Ledger)\s*[!=]=`), why: settleWhy},
	{pattern: regexp.MustCompile(`SelfContainedBase\s*!=\s*""`), why: "tests prompt-file presence as a kind test -- read Tracker instead"},
	// == "" is legitimate only as the --self-contained capability check,
	// which always pairs it with the non-negated selfContained flag.
	{pattern: regexp.MustCompile(`SelfContainedBase\s*==\s*""`), unless: regexp.MustCompile(`(^|[^!\w.])(\w+\.)*[sS]elfContained\s*&&`), why: "tests prompt-file absence as a kind test -- read Tracker instead"},
	{pattern: regexp.MustCompile(`\.Labels\s*[!=]=\s*dispatchkind\.`), why: "compares the label family -- read Tracker instead"},
	{pattern: regexp.MustCompile(`dispatchkind\.Labels(Research|Configured|None)\s*[!=]=`), why: "compares the label family -- read Tracker instead"},
}

var findingLabelLiteral = regexp.MustCompile(`"agent-(review|research|butler)-finding"`)

// TestKindAxesGuard walks the launcher module tree (prior art: the marker and
// parity checks) and fails on any of the old kind == kind comparisons the
// descriptor axes replaced, so a reintroduced comparison is caught here
// instead of drifting back in unnoticed.
func TestKindAxesGuard(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != "." && (name == "dispatchkind" || name == "testdata" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		underSettle := strings.Contains(filepath.ToSlash(path), "internal/settle/")
		for i, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue // prose mentioning an axis's old name isn't a comparison
			}
			for _, fp := range kindAxisForbiddenPatterns {
				if fp.matches(line) {
					t.Errorf("%s:%d: %s\n\t%s", path, i+1, fp.why, trimmed)
				}
			}
			if underSettle && findingLabelLiteral.MatchString(line) {
				t.Errorf("%s:%d: finding-label literal instead of the FindingLabel axis\n\t%s", path, i+1, trimmed)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk .: %v", err)
	}
}

// TestKindAxesGuardPatterns pins which spellings the guard treats as a kind
// test, so a loosened or over-broad pattern shows up here rather than as a
// silently passing or spuriously failing walk.
func TestKindAxesGuardPatterns(t *testing.T) {
	forbidden := []string{
		`if c.kind().Settle == dispatchkind.SettleVerdict {`,
		`if dispatchkind.SettleVerdict == c.kind().Settle {`,
		`if d.Settle != dispatchkind.SettleMerge {`,
		`if d.Prompts.SelfContainedBase != "" {`,
		`if d.Prompts.SelfContainedBase == "" {`,
		`if !c.selfContained && d.Prompts.SelfContainedBase == "" {`,
		`if d.Labels == dispatchkind.LabelsResearch {`,
		`if dispatchkind.LabelsNone != d.Labels {`,
	}
	allowed := []string{
		`switch c.kind().Settle {`,
		`if r.Settle == nil {`,
		`if c.selfContained && c.kind().Prompts.SelfContainedBase == "" {`,
		`if e.SelfContained && d.Prompts.Base != "" && d.Prompts.SelfContainedBase == "" {`,
		`if d.Labels == ownFamily {`,
	}
	hit := func(line string) bool {
		for _, fp := range kindAxisForbiddenPatterns {
			if fp.matches(line) {
				return true
			}
		}
		return false
	}
	for _, line := range forbidden {
		if !hit(line) {
			t.Errorf("guard misses kind test: %s", line)
		}
	}
	for _, line := range allowed {
		if hit(line) {
			t.Errorf("guard flags legitimate use: %s", line)
		}
	}
}
