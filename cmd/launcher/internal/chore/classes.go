package chore

import (
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/signalwire"
)

// parseClasses parses one of two host-side class allow-lists sharing this
// grammar: BUTLER_CHORE_CLASSES (ADR 0056, the promotion allow-list) or
// BUTLER_PATCH_CLASSES (ADR 0057, the patch-rung allow-list) -- a
// space-separated list of "<chore>=<class>[,<class>...]" entries. Neither
// input reaches the Box as-is: BUTLER_CHORE_CLASSES itself never crosses
// (schema key butlerChoreClasses has boxEnv = false), while
// BUTLER_PATCH_CLASSES's parsed result is re-rendered and forwarded as
// CHORE_PATCH_CLASSES once the patch room allows it (internal/butler). ""
// parses to an empty map. Load cross-checks the result against BUTLER_CHORES.
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
