package chore

import (
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/signalwire"
)

// parseClasses parses BUTLER_CHORE_CLASSES (ADR 0056): a space-separated list
// of "<chore>=<class>[,<class>...]" entries, the host-side allow-list of
// finding classes each Chore may auto-promote. The Box never sees this value
// (schema key butlerChoreClasses has boxEnv = false) and cannot influence it.
// "" parses to an empty map. Load cross-checks the result against
// BUTLER_CHORES.
func parseClasses(s string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, entry := range strings.Fields(s) {
		chore, classesPart, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("invalid entry %q: missing '='", entry)
		}
		if !promptassembly.ValidChoreName(chore) {
			return nil, fmt.Errorf("invalid chore name %q: %s", chore, promptassembly.ChoreNameRule)
		}
		if _, dup := out[chore]; dup {
			return nil, fmt.Errorf("duplicate chore %q", chore)
		}
		classes := strings.Split(classesPart, ",")
		seen := map[string]bool{}
		for _, class := range classes {
			if !signalwire.ValidClass(class) {
				return nil, fmt.Errorf("invalid class %q for chore %q: %s", class, chore, signalwire.ClassRule)
			}
			if seen[class] {
				return nil, fmt.Errorf("duplicate class %q for chore %q", class, chore)
			}
			seen[class] = true
		}
		out[chore] = classes
	}
	return out, nil
}
