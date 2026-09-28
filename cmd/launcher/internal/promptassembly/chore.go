package promptassembly

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"spindrift.dev/launcher/internal/dispatchkind"
)

var choreNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ChoreNameRule describes choreNameRe for error messages; keep the two in step.
const ChoreNameRule = "must contain only letters, digits, '-', and '_'"

// ValidChoreName reports whether s is one or more letters, digits, dash, and
// underscore -- the one shape a ChoreName must satisfy before it can safely reach
// filepath.Join. No path separator or ".." can ever cross this gate before
// choreSection builds the chores/<name>.md lookup path (ADR 0056, issue
// #3875). internal/butler bounds BUTLER_CHORE_CLASSES names with it too.
func ValidChoreName(s string) bool {
	return choreNameRe.MatchString(s)
}

// choreSection renders the ${CHORE_PROMPT} substitution value: the named
// Chore's own prompt file under PromptsDir/chores/<name>.md. Every kind other
// than the butler (Keying != ByChore) has no chores directory contract to
// honor and renders "". Unlike issueTextSection's silent-empty default, a
// ByChore kind with a malformed or unresolvable ChoreName fails assembly
// outright: the butler always names exactly one Chore, so a missing prompt
// file is a configuration error, never a legitimately empty case.
func choreSection(e Env) (string, error) {
	d := e.descriptor()
	if d.Keying != dispatchkind.ByChore {
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
