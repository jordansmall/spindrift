package daemon

import (
	"fmt"

	"spindrift.dev/launcher/internal/dispatchkind"
)

// Kind is the Dispatch kind the daemon drives: work dispatch, advise-only
// research, or the advise-only butler (ADR 0056). All three are driven
// through the same exit-code interpretation (ADR 0022), which is why one
// loop drives them all.
type Kind string

// KindOf is d's daemon Kind: its Verb, the string the daemon's bookkeeping
// and Event JSON carry.
func KindOf(d *dispatchkind.Descriptor) Kind {
	return Kind(d.Verb)
}

// choreKeyed reports whether k's records key by Ledger Chore rather than
// tracker issue; false for a Kind that names no descriptor.
func (k Kind) choreKeyed() bool {
	d, ok := dispatchkind.ByVerb(string(k))
	return ok && d.Keying == dispatchkind.ByChore
}

// ParseKind parses a CLI/config kind string. "" defaults to work dispatch so
// existing dispatch-only callers need not pass a kind at all.
func ParseKind(s string) (Kind, error) {
	if s == "" {
		return KindOf(dispatchkind.Work), nil
	}
	if d, ok := dispatchkind.ByVerb(s); ok {
		return KindOf(d), nil
	}
	return "", fmt.Errorf("unknown kind %q", s)
}

// ParseKinds parses the daemon's argv verb as a set of kinds to draw from one
// pool (issue #3541). Unlike ParseKind, "" here means "every kind" rather
// than "dispatch": a bare daemon invocation now runs work, research, and the
// butler off the same pool by default (ADR 0056, #3878), while "dispatch",
// "research", or "butler" alone still restrict it to one kind for callers
// that want that. The daemon binary still drops the butler from "" when
// BUTLER_CHORES enables no Chore (cmd/launcher/daemon's gateKinds).
func ParseKinds(s string) ([]Kind, error) {
	if s == "" {
		kinds := make([]Kind, 0, len(dispatchkind.All))
		for _, d := range dispatchkind.All {
			kinds = append(kinds, KindOf(d))
		}
		return kinds, nil
	}
	if d, ok := dispatchkind.ByVerb(s); ok {
		return []Kind{KindOf(d)}, nil
	}
	return nil, fmt.Errorf("unknown kind %q", s)
}
