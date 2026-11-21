package promptassembly

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"spindrift.dev/launcher/internal/dispatchkind"
)

var choreNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*$`)

// ChoreNameRule describes choreNameRe for error messages; keep the two in step.
const ChoreNameRule = "must contain only letters, digits, '-', and '_', not starting with '-'"

// ValidChoreName reports whether s matches choreNameRe: no path separator or
// ".." can reach choreSection's filepath.Join (ADR 0056), and --chore can
// never mistake it for a flag (issue #4095). internal/chore bounds
// BUTLER_CHORE_CLASSES names with it too.
func ValidChoreName(s string) bool {
	return choreNameRe.MatchString(s)
}

// choreSection renders ${CHORE_PROMPT} from PromptsDir/chores/<name>.md, or
// "" for a kind not keyed ByChore. Unlike issueTextSection, a bad or missing
// ChoreName fails assembly: the butler always names exactly one Chore, so a
// missing prompt file is a configuration error.
func choreSection(e Env) (string, error) {
	d := e.descriptor()
	switch d.Keying {
	case dispatchkind.ByChore:
	default:
		return "", nil
	}
	if !ValidChoreName(e.ChoreName) {
		return "", fmt.Errorf("promptassembly: invalid CHORE_NAME %q: %s", e.ChoreName, ChoreNameRule)
	}
	path := filepath.Join(e.PromptsDir, "chores", e.ChoreName+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("promptassembly: unknown chore %q (%s): %w", e.ChoreName, path, err)
	}
	return strings.TrimRight(string(data), "\n"), nil
}
