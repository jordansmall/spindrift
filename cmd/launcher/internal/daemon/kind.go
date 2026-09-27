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

// KindDispatch, KindResearch, and KindButler are dispatchkind.Work/Research/
// Butler's verbs (issue #3872): the descriptors are the source of truth,
// these are just the daemon's own Kind-typed aliases for them.
var (
	KindDispatch = Kind(dispatchkind.Work.Verb)
	KindResearch = Kind(dispatchkind.Research.Verb)
	KindButler   = Kind(dispatchkind.Butler.Verb)
)

// ParseKind parses a CLI/config kind string. "" defaults to KindDispatch so
// existing dispatch-only callers need not pass a kind at all.
func ParseKind(s string) (Kind, error) {
	if s == "" {
		return KindDispatch, nil
	}
	if d, ok := dispatchkind.ByVerb(s); ok {
		return Kind(d.Verb), nil
	}
	return "", fmt.Errorf("daemon: unknown kind %q", s)
}

// ParseKinds parses the daemon's argv verb as a set of kinds to draw from one
// pool (issue #3541). Unlike ParseKind, "" here means "every kind" rather
// than "dispatch": a bare daemon invocation now runs work, research, and the
// butler off the same pool by default (ADR 0056, #3878), while "dispatch",
// "research", or "butler" alone still restrict it to one kind for callers
// that want that. The daemon binary still drops the butler from "" when
// BUTLER_CHORES enables no Chore (cmd/launcher/daemon's gateButlerKind).
func ParseKinds(s string) ([]Kind, error) {
	if s == "" {
		kinds := make([]Kind, 0, len(dispatchkind.All))
		for _, d := range dispatchkind.All {
			kinds = append(kinds, Kind(d.Verb))
		}
		return kinds, nil
	}
	if d, ok := dispatchkind.ByVerb(s); ok {
		return []Kind{Kind(d.Verb)}, nil
	}
	return nil, fmt.Errorf("daemon: unknown kind %q", s)
}
