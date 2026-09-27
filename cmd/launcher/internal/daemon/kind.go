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
// pool (issue #3541). Unlike ParseKind, "" here means "both" rather than
// "dispatch": a bare daemon invocation now runs work and research off the
// same pool by default, while "dispatch" or "research" alone still restrict
// it to one kind for callers that want that.
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
