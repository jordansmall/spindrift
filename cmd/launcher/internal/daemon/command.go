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
//
// The flakeref is built before the kind is parsed, so a spec that is wrong in
// both ways reports the flakeref error: the pin is what keeps the daemon off a
// moving working tree, so it is the half worth naming first. TestChildCommand
// pins that order.
func ChildCommand(s ChildSpec) ([]string, error) {
	flakeref, err := appFlakeref(s.RepoPath, s.AppAttr, s.Revision, "a child must always be pinned")
	if err != nil {
		return nil, err
	}
	kind, err := ParseKind(string(s.Kind))
	if err != nil {
		return nil, err
	}

	return []string{
		"nix", "run", flakeref, "--",
		string(kind),
		// The pool cap now lives in the daemon (one slot, one child), so
		// each child must itself be exactly one Box: --max-jobs 1 caps the
		// wave to a single issue and --max-parallel 1 caps concurrency
		// within it. Pinning both means the guarantee does not depend on
		// which of the two knobs a given dispatch path happens to honour.
		"--max-jobs", "1",
		"--max-parallel", "1",
	}, nil
}

// appFlakeref validates repoPath/revision and pins attr's frag into a git
// flakeref. Shared by ChildCommand and DoctorCommand: both run an app at the
// default-or-named attr with the same empty-frag-defaults, same "#?&"
// rejection — unlike SelfCommand's SelfAttr, which rejects an empty attr
// rather than defaulting it (see SelfCommand's comment), so SelfCommand
// keeps its own frag handling rather than sharing this helper. unpinnedMsg
// is each call site's own wording for the empty-revision error.
func appFlakeref(repoPath, attr, revision, unpinnedMsg string) (string, error) {
	if err := validateRepoPathAndRevision(repoPath, revision, unpinnedMsg); err != nil {
		return "", err
	}
	// The fragment is omitted entirely for the default attr rather than
	// emitting a bare trailing "#", which nix would reject. frag is
	// interpolated straight after that "#", so it gets the same
	// malformed-ref check repoPath got above (the ".#" prefix itself is
	// stripped first, so a bare default attr never trips it).
	frag := strings.TrimPrefix(attr, ".#")
	if frag != "" && strings.ContainsAny(frag, "#?&") {
		return "", fmt.Errorf("daemon: app attr must not contain '#', '?', or '&', got %q", attr)
	}
	return pinnedFlakeref(repoPath, revision, frag), nil
}

// validateRepoPathAndRevision runs the checks appFlakeref (on behalf of
// ChildCommand and DoctorCommand) and SelfCommand both need before touching
// repoPath or revision: repoPath must be an absolute path free of the chars
// that would open a second query string, fragment, or query param once
// interpolated into the flakeref (pinnedFlakeref), and revision must be
// non-empty — unpinnedMsg supplies each call site's own wording for why. It
// stops short of the two attr/frag checks: those diverge on empty-frag policy
// (appFlakeref's attr defaults on empty, SelfCommand's SelfAttr rejects it),
// so the honest shared core is only these three checks, not the whole
// validation.
func validateRepoPathAndRevision(repoPath, revision, unpinnedMsg string) error {
	if repoPath == "" || !path.IsAbs(repoPath) {
		return fmt.Errorf("daemon: repo path must be an absolute path, got %q", repoPath)
	}
	if revision == "" {
		return fmt.Errorf("daemon: revision must not be empty (%s)", unpinnedMsg)
	}
	if strings.ContainsAny(repoPath, "#?&") {
		return fmt.Errorf("daemon: repo path must not contain '#', '?', or '&', got %q", repoPath)
	}
	return nil
}

// pinnedFlakeref builds a git flakeref pinned to revision at repoPath, with
// frag (already stripped of its leading ".#") appended as the fragment when
// non-empty. Shared by SelfCommand and, through appFlakeref, by ChildCommand
// and DoctorCommand: all three need the exact same pin, for the exact same
// reason.
//
// git+file:// (not a bare path) because only a git flakeref accepts ?rev=,
// and that pin is the whole point: the daemon must never evaluate a moving
// working tree. allRefs=1 because the resolved tip lives on a
// remote-tracking ref the local checkout's own branch may lag behind.
func pinnedFlakeref(repoPath, revision, frag string) string {
	flakeref := fmt.Sprintf("git+file://%s?rev=%s&allRefs=1", repoPath, revision)
	if frag != "" {
		flakeref += "#" + frag
	}
	return flakeref
}

// SelfSpec is everything one self-build evaluation needs.
type SelfSpec struct {
	RepoPath string // absolute path to the operator's checkout
	SelfAttr string // DAEMON_SELF_APP, e.g. ".#daemon"
	Revision string // full git rev to evaluate at
	System   string // nix system double, e.g. "x86_64-linux"
}

// SelfCommand builds the argv that evaluates the daemon's own program store
// path at a pinned revision.
func SelfCommand(s SelfSpec) ([]string, error) {
	if err := validateRepoPathAndRevision(s.RepoPath, s.Revision, "a self-check must always be pinned"); err != nil {
		return nil, err
	}
	if s.System == "" {
		return nil, fmt.Errorf("daemon: system must not be empty")
	}
	frag := strings.TrimPrefix(s.SelfAttr, ".#")
	// Unlike ChildCommand's AppAttr, an empty attr here is an error rather
	// than a default: "apps.<system>..program" is a malformed attribute
	// path, not nix's default search path (that's ChildCommand's flakeref
	// fragment, which nix run itself defaults for; nix eval has no such
	// default to fall back on).
	if frag == "" {
		return nil, fmt.Errorf("daemon: self attr must not be empty, got %q", s.SelfAttr)
	}
	if strings.ContainsAny(frag, "#?&") {
		return nil, fmt.Errorf("daemon: self attr must not contain '#', '?', or '&', got %q", s.SelfAttr)
	}

	// The fragment is the full attribute path (apps.<system>.<attr>.program),
	// not the bare attr ChildCommand passes: nix eval's own fragment
	// resolution only tries packages.<system>/legacyPackages.<system> --
	// apps is nix run's default search path, not nix eval's -- so the
	// system double has to be supplied rather than inferred by nix.
	frag = fmt.Sprintf("apps.%s.%s.program", s.System, frag)

	return []string{"nix", "eval", "--raw", pinnedFlakeref(s.RepoPath, s.Revision, frag)}, nil
}

// DoctorSpec is everything one pinned startup-preflight invocation needs.
type DoctorSpec struct {
	RepoPath string // absolute path to the operator's checkout
	AppAttr  string // DAEMON_APP, e.g. ".#" or ".#dogfood-bwrap"
	Revision string // full git rev the preflight is pinned to
}

// DoctorCommand builds the argv for the daemon's own startup preflight: the
// same pinned flakeref ChildCommand resolves (same repo/attr/revision, same
// validation), but invoking "doctor" instead of a dispatch kind, and with
// neither --max-jobs nor --max-parallel appended — those cap a child's wave
// of dispatched work, and doctor dispatches nothing to cap.
func DoctorCommand(s DoctorSpec) ([]string, error) {
	flakeref, err := appFlakeref(s.RepoPath, s.AppAttr, s.Revision, "a preflight must always be pinned")
	if err != nil {
		return nil, err
	}
	return []string{"nix", "run", flakeref, "--", "doctor"}, nil
}
