package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/testutil/repopath"
)

// Guards box and agent/entrypoint.sh against branching on the kind name
// (issue #3996, ADR 0056): the Box reads DISPATCH_KEYING /
// DISPATCH_ANNOUNCE_VERB / DISPATCH_KEY, the axes dispatch.buildBoxEnv
// resolved host-side from the kind's descriptor, and the advise-only posture
// from dispatchkind.ByName(...).AdviseOnly -- never the kind name itself.
// Prior art: ../kindaxes_guard_test.go.

// shellKindBranch mirrors the grep -E patterns of the retired
// tests/entrypoint-kind-axes.bats. `--dispatch-kind "${DISPATCH_KIND:-work}"`
// (handing the name to driver-exec for its own descriptor lookup) and the
// "DISPATCH_KIND=${DISPATCH_KIND:-work}" display string are deliberately not
// comparisons: both lack an operator directly after the expansion.
var shellKindBranch = []*regexp.Regexp{
	regexp.MustCompile(`\$\{?DISPATCH_KIND[^}]*\}?"?[[:space:]]+[!=]?=`),
	regexp.MustCompile(`case[[:space:]]+"?\$\{?DISPATCH_KIND`),
	regexp.MustCompile(`[!=]?=[[:space:]]*"?(butler|research|work)"?([^A-Za-z0-9_.-]|$)`),
	// The "butler-" key derivation is dispatch.buildBoxEnv's job
	// (DISPATCH_KEY), not the Box's.
	regexp.MustCompile(`"butler-`),
}

// goKindBranch is the Go-side equivalent. `r.kind == ""` (the empty default)
// is not a kind-name comparison and does not match.
var goKindBranch = []*regexp.Regexp{
	regexp.MustCompile(`(DispatchKind|\bkind|\.Name)\s*[!=]=\s*"(work|research|butler)"`),
	regexp.MustCompile(`"(work|research|butler)"\s*[!=]=\s*[\w.]*(DispatchKind|kind|\.Name)\b`),
	regexp.MustCompile(`switch\s+([\w.]*\.)?(DispatchKind|kind)\s*\{`),
	regexp.MustCompile(`"butler-`),
}

// kindBranchViolations returns the non-comment lines of src that match any
// pattern. Full-line comments only are skipped, so prose mentioning a kind
// name is not a comparison.
func kindBranchViolations(src, commentPrefix string, patterns []*regexp.Regexp) []string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), commentPrefix) {
			continue
		}
		for _, p := range patterns {
			if p.MatchString(line) {
				out = append(out, line)
				break
			}
		}
	}
	return out
}

func TestEntrypointHasNoKindBranch(t *testing.T) {
	entrypoint := filepath.Join(repopath.PromptsDir(), "..", "..", "..", "agent", "entrypoint.sh")
	raw, err := os.ReadFile(entrypoint)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range kindBranchViolations(string(raw), "#", shellKindBranch) {
		t.Errorf("agent/entrypoint.sh branches on the kind name: %s", v)
	}
}

func TestBoxHasNoKindBranch(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, v := range kindBranchViolations(string(raw), "//", goKindBranch) {
			t.Errorf("%s branches on the kind name (read dispatchkind.ByName(...).AdviseOnly or a DISPATCH_* axis instead): %s", f, strings.TrimSpace(v))
		}
	}
	if checked == 0 {
		t.Fatal("no non-test .go files found: the guard is walking the wrong directory")
	}
}

func TestKindBranchCheckerCatchesReintroducedViolations(t *testing.T) {
	cases := []struct {
		name, prefix, src, want string
		patterns                []*regexp.Regexp
	}{
		{"shell DISPATCH_KIND comparison", "#", `#!/usr/bin/env bash
# a plain comment mentioning butler is fine
if [ "${DISPATCH_KIND:-}" = "butler" ]; then
  echo sweeping
fi
`, `DISPATCH_KIND:-}" = "butler"`, shellKindBranch},
		{"shell case-on-kind dispatch", "#", `#!/usr/bin/env bash
case "$DISPATCH_KIND" in
  research) echo researching ;;
  *) echo implementing ;;
esac
`, `case "$DISPATCH_KIND"`, shellKindBranch},
		{"shell bare butler- key literal", "#", `#!/usr/bin/env bash
export BRANCH="butler-${CHORE_NAME}"
`, `"butler-`, shellKindBranch},
		{"go kind-name comparison", "//", `package main

// if r.kind == "work" in a comment is fine
func f(r runner) bool {
	if r.kind == "research" {
		return true
	}
	return false
}
`, `r.kind == "research"`, goKindBranch},
		{"go bare local kind comparison", "//", `package main

func f(kind string) bool {
	if kind == "butler" {
		return true
	}
	return false
}
`, `kind == "butler"`, goKindBranch},
		{"go reversed bare local kind comparison", "//", `package main

func f(kind string) bool {
	return "work" != kind
}
`, `"work" != kind`, goKindBranch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := kindBranchViolations(c.src, c.prefix, c.patterns)
			if len(got) != 1 || !strings.Contains(got[0], c.want) {
				t.Errorf("violations = %q, want exactly one containing %q", got, c.want)
			}
		})
	}
}

func TestKindBranchCheckerAllowsLegitimateSpellings(t *testing.T) {
	shell := `exec driver-exec --dispatch-kind "${DISPATCH_KIND:-work}"
echo "DISPATCH_KIND=${DISPATCH_KIND:-work}"
`
	if got := kindBranchViolations(shell, "#", shellKindBranch); len(got) != 0 {
		t.Errorf("shell false positives: %q", got)
	}
	goSrc := "\tif r.kind == \"\" {\n\t\tr.kind = defaultKind\n\t}\n"
	if got := kindBranchViolations(goSrc, "//", goKindBranch); len(got) != 0 {
		t.Errorf("go false positives: %q", got)
	}
}
