package daemon

import "fmt"

// Kind is the Dispatch kind the daemon drives: work dispatch or advise-only
// research. Both are driven through the same exit-code interpretation (ADR
// 0022), which is why one loop drives either — but the contract is not
// identical: research's bootstrap failures surface as a bare 1 (main.go's
// "research" handler), not exitConfigInvalid, which the loop treats as a
// generic failure.
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
