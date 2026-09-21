package daemon

import "fmt"

// Kind is the Dispatch kind the daemon drives: work dispatch or advise-only
// research. Both are driven through the same exit-code interpretation (ADR
// 0022), which is why one loop drives either.
type Kind string

const (
	KindDispatch Kind = "dispatch"
	KindResearch Kind = "research"
)

// ParseKind parses a CLI/config kind string. "" defaults to KindDispatch so
// existing dispatch-only callers need not pass a kind at all.
func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case "":
		return KindDispatch, nil
	case KindDispatch, KindResearch:
		return Kind(s), nil
	default:
		return "", fmt.Errorf("daemon: unknown kind %q", s)
	}
}

// ParseKinds parses the daemon's argv verb as a set of kinds to draw from one
// pool (issue #3541). Unlike ParseKind, "" here means "both" rather than
// "dispatch": a bare daemon invocation now runs work and research off the
// same pool by default, while "dispatch" or "research" alone still restrict
// it to one kind for callers that want that.
func ParseKinds(s string) ([]Kind, error) {
	switch Kind(s) {
	case "":
		return []Kind{KindDispatch, KindResearch}, nil
	case KindDispatch, KindResearch:
		return []Kind{Kind(s)}, nil
	default:
		return nil, fmt.Errorf("daemon: unknown kind %q", s)
	}
}
