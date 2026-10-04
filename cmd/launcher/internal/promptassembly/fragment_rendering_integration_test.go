//go:build integration

package promptassembly_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/seamtest"
	"spindrift.dev/launcher/internal/testutil/repopath"
)

// section returns prompt from the line "from" up to (excluding) the line "to",
// or to the end when "to" is empty.
func section(t *testing.T, prompt, from, to string) string {
	t.Helper()
	start := strings.Index(prompt, "\n"+from+"\n")
	if start < 0 {
		t.Fatalf("prompt has no %q section", from)
	}
	rest := prompt[start+1:]
	if to == "" {
		return rest
	}
	end := strings.Index(rest, "\n"+to+"\n")
	if end < 0 {
		t.Fatalf("prompt has no %q heading after %q", to, from)
	}
	return rest[:end]
}

// These cases need the injected outcome-contract block, which only the seam
// fixtures carry, so they run through assembleCell rather than Assemble.
func TestAssembleOutcomeRendering(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(*cellInputs)
		check   func(t *testing.T, prompt string)
		wantNot string
	}{
		{
			name: "read-write OUTCOME landing keeps the pr-url placeholder",
			check: func(t *testing.T, prompt string) {
				if !strings.Contains(prompt, "landing=<pr-url> status=ready") {
					t.Error("prompt missing the pr-url landing line")
				}
				if !regexp.MustCompile(`(?m)status=ready note=<short reason>$`).MatchString(prompt) {
					t.Error("prompt has no line ending status=ready note=<short reason>")
				}
			},
		},
		{
			name:  "read-only OUTCOME landing reports the branch, never a pr-url",
			setup: (*cellInputs).readOnly,
			check: func(t *testing.T, prompt string) {
				if !strings.Contains(prompt, "landing=agent/issue-7 status=ready") {
					t.Error("prompt missing the branch landing line")
				}
				if !regexp.MustCompile(`(?m)status=ready note=<short reason>$`).MatchString(prompt) {
					t.Error("prompt has no line ending status=ready note=<short reason>")
				}
				if strings.Contains(section(t, prompt, "# OUTCOME", "# IF BLOCKED"), "landing=<pr-url>") {
					t.Error("read-only OUTCOME section names a landing=<pr-url> placeholder")
				}
			},
		},
		{
			name: "default prompt delegates to scout and ends with ready or blocked outcomes",
			check: func(t *testing.T, prompt string) {
				for _, want := range []string{"scout", "SPINDRIFT_OUTCOME", "status=blocked", "status=ready"} {
					if !strings.Contains(strings.ToLower(prompt), strings.ToLower(want)) {
						t.Errorf("prompt missing %q", want)
					}
				}
				if strings.Contains(prompt, "status=merged") {
					t.Error("prompt names status=merged, the launcher's status, not the Driver's")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := defaultCell()
			if tc.setup != nil {
				tc.setup(c)
			}
			_, out := assembleCell(t, c, t.TempDir())
			tc.check(t, string(mustRead(t, out.Prompt)))
		})
	}
}

// Every registry row names a fragment file the prompts tree must ship; a row
// pointing at a missing file would render silently empty.
func TestRegistryRowsShipAsFragmentFiles(t *testing.T) {
	reg, err := promptassembly.LoadRegistryFile(seamtest.Path(t, "fragments-registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Rows) == 0 {
		t.Fatal("registry has no rows, check is vacuous")
	}
	for _, row := range reg.Rows {
		if _, err := os.Stat(filepath.Join(repopath.PromptsDir(), "fragments", row.Fragment)); err != nil {
			t.Errorf("registry row %q: %v", row.Fragment, err)
		}
	}
}
