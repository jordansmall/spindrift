package daemon

import (
	"fmt"
	"path"
	"strings"
)

// ChildSpec is everything one pinned child invocation needs.
type ChildSpec struct {
	RepoPath string // absolute path to the operator's checkout
	AppAttr  string // DAEMON_APP, e.g. ".#" or ".#dogfood-bwrap"
	Revision string // full git rev the child is pinned to
	Kind     Kind
}

// ChildCommand builds the argv for one pinned child launcher invocation. It
// is pure: no filesystem or process access, so the loop slice's seam is
// tested by feeding it a ChildSpec and asserting on the returned argv.
func ChildCommand(s ChildSpec) ([]string, error) {
	if s.RepoPath == "" || !path.IsAbs(s.RepoPath) {
		return nil, fmt.Errorf("daemon: repo path must be an absolute path, got %q", s.RepoPath)
	}
	if s.Revision == "" {
		return nil, fmt.Errorf("daemon: revision must not be empty (a child must always be pinned)")
	}
	// RepoPath is interpolated straight into the flakeref below; any of
	// these three chars would open a second query string, fragment, or
	// query param, producing a malformed ref instead of a clear error.
	if strings.ContainsAny(s.RepoPath, "#?&") {
		return nil, fmt.Errorf("daemon: repo path must not contain '#', '?', or '&', got %q", s.RepoPath)
	}
	kind, err := ParseKind(string(s.Kind))
	if err != nil {
		return nil, err
	}

	// git+file:// (not a bare path) because only a git flakeref accepts
	// ?rev=, and that pin is the whole point: the daemon must never
	// evaluate a moving working tree. allRefs=1 because the resolved tip
	// lives on a remote-tracking ref the local checkout's own branch may
	// lag behind.
	flakeref := fmt.Sprintf("git+file://%s?rev=%s&allRefs=1", s.RepoPath, s.Revision)

	// The fragment is omitted entirely for the default attr rather than
	// emitting a bare trailing "#", which nix would reject. frag is
	// interpolated straight after that "#", so it gets the same
	// malformed-ref check RepoPath got above (the ".#" prefix itself is
	// stripped first, so a bare default attr never trips it).
	if frag := strings.TrimPrefix(s.AppAttr, ".#"); frag != "" {
		if strings.ContainsAny(frag, "#?&") {
			return nil, fmt.Errorf("daemon: app attr must not contain '#', '?', or '&', got %q", s.AppAttr)
		}
		flakeref += "#" + frag
	}

	return []string{
		"nix", "run", flakeref, "--",
		string(kind),
	}, nil
}
