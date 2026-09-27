package daemon

import (
	"fmt"

	"spindrift.dev/launcher/internal/dispatchkind"
)

// Kind is the Dispatch kind the daemon drives: work dispatch or advise-only
// research. Both are driven through the same exit-code interpretation (ADR
// 0022), which is why one loop drives either.
type Kind string

// KindDispatch and KindResearch are dispatchkind.Work/Research's verbs
// (issue #3872): the descriptors are the source of truth, these are just
// the daemon's own Kind-typed aliases for them.
var (
	KindDispatch = Kind(dispatchkind.Work.Verb)
	KindResearch = Kind(dispatchkind.Research.Verb)
)

// driven reports whether the daemon drives d at all: PriorityUndriven kinds
// (the butler, ADR 0056) have no daemon support yet, so ParseKind/ParseKinds
// reject them by name exactly like an unrecognized verb -- a kind gains a
// pool slot only once its own daemon-support ticket lands.
func driven(d *dispatchkind.Descriptor) bool {
	return d.DaemonPriority != dispatchkind.PriorityUndriven
}

// ParseKind parses a CLI/config kind string. "" defaults to KindDispatch so
// existing dispatch-only callers need not pass a kind at all.
func ParseKind(s string) (Kind, error) {
	if s == "" {
		return KindDispatch, nil
	}
	if d, ok := dispatchkind.ByVerb(s); ok && driven(d) {
		return Kind(d.Verb), nil
	}
	return "", fmt.Errorf("daemon: unknown kind %q", s)
}

// ParseKinds parses the daemon's argv verb as a set of kinds to draw from one
// pool (issue #3541). Unlike ParseKind, "" here means "every driven kind"
// rather than "dispatch": a bare daemon invocation now runs work and research
// off the same pool by default, while "dispatch" or "research" alone still
// restrict it to one kind for callers that want that. An undriven kind
// (butler, ADR 0056) is left out of the "" set and rejected by name the same
// as an unrecognized verb -- see driven.
func ParseKinds(s string) ([]Kind, error) {
	if s == "" {
		kinds := make([]Kind, 0, len(dispatchkind.All))
		for _, d := range dispatchkind.All {
			if !driven(d) {
				continue
			}
			kinds = append(kinds, Kind(d.Verb))
		}
		return kinds, nil
	}
	if d, ok := dispatchkind.ByVerb(s); ok && driven(d) {
		return []Kind{Kind(d.Verb)}, nil
	}
	return nil, fmt.Errorf("daemon: unknown kind %q", s)
}
