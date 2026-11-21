package daemon

import (
	"fmt"
	"os"
	"path"
	"strings"
)

// ReportFD is the file descriptor SPINDRIFT_REPORT_FD names in a child's
// environment: the only exec.Cmd.ExtraFiles entry lands at fd 3. The host
// runner's pipe wiring and this env value must agree, which is why both
// point here.
const ReportFD = 3

// reportFDEnv is the environment variable name that carries ReportFD to a
// child; not exported, since only ChildCommand ever sets it.
const reportFDEnv = "SPINDRIFT_REPORT_FD"

// PoolChildEnv marks a daemon pool child's environment (issue #7342). The
// child's reconcileAfterDispatch skips the late-merge sweep when it is set,
// because the daemon runs that sweep itself (LateMergeSweepVerb) outside the
// pool's slots. Launcher-internal like SPINDRIFT_REPORT_FD, not a knob: the
// launcher rejects knob names in its environment, so a marker cannot reuse one.
const PoolChildEnv = "SPINDRIFT_DAEMON_POOL_CHILD"

// InPoolChild reads the PoolChildEnv marker here, not inline at callers, so the
// launcher spells the marker key in one place.
func InPoolChild() bool { return os.Getenv(PoolChildEnv) != "" }

// LateMergeSweepVerb is the hidden launcher verb the daemon execs for the
// late-merge sweep, spelled once here for main.go's dispatch and the daemon's
// command builder.
const LateMergeSweepVerb = "__late-merge-sweep"

// ChildSpec is everything one pinned child invocation needs.
type ChildSpec struct {
	RepoPath string // absolute path to the operator's checkout
	AppAttr  string // DAEMON_APP, e.g. ".#" or ".#dogfood-bwrap"
	Revision string // full git rev the child is pinned to
	Kind     Kind
	Env      []string // the daemon's own environment, os.Environ() "KEY=VALUE" shape
	Knobs    []string // keys of the Launcher input document's settings map
	// FeatureBranch is the branch children target via --base-branch; see
	// appendFeatureBranch.
	FeatureBranch string
}

// Command is the argv and environment for one pinned invocation, returned
// together so a caller cannot transpose two same-typed []string results.
// With a nil error Env is non-nil (see withoutKeys for why that matters).
type Command struct {
	Argv []string
	Env  []string
}

// appendFeatureBranch appends "--base-branch <featureBranch>" to argv when
// featureBranch is non-empty, for both ChildCommand (every kind, including
// research) and DoctorCommand: empty means no flag, so the invocation keeps
// its baked BASE_BRANCH.
func appendFeatureBranch(argv []string, featureBranch string) []string {
	if featureBranch == "" {
		return argv
	}
	return append(argv, "--base-branch", featureBranch)
}

// signalCarrierEnv is the env-only BOX_SIGNAL_CARRIER knob (flag
// --box-signal-carrier), spelled out here; nix/checks/schema-drift.nix pins
// both literals to lib/env-schema.nix's signalCarrier row.
const signalCarrierEnv = "BOX_SIGNAL_CARRIER"

// liftSignalCarrier strips signalCarrierEnv from env and, when its value is
// non-empty, appends "--box-signal-carrier <value>" to argv. The launcher
// exits config-invalid on any non-secret knob in its environment, so the
// value must travel as a flag; empty means unset, leaving the child its
// schema default.
func liftSignalCarrier(argv, env []string) ([]string, []string) {
	prefix := signalCarrierEnv + "="
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, prefix); ok && v != "" {
			argv = append(argv, "--box-signal-carrier", v)
		}
	}
	return argv, withoutKeys(env, []string{signalCarrierEnv})
}

// ChildCommand builds the Command for one pinned child launcher invocation.
// It is pure: the caller supplies the environment and the knob keys to
// strip; it never reads os.Environ.
//
// The child's environment is the daemon's minus every key in Knobs: those
// are the Launcher input document's settings, the child's own knob source,
// so a leaked daemon value would shadow the child's resolution.
// BOX_SIGNAL_CARRIER reaches the child as a flag (see liftSignalCarrier).
//
// The flakeref is built before the kind is parsed, so a spec wrong both ways
// reports the flakeref error: the pin, which keeps the daemon off a moving
// working tree, is the half worth naming first. TestChildCommand pins that
// order.
func ChildCommand(s ChildSpec) (Command, error) {
	flakeref, err := appFlakeref(s.RepoPath, s.AppAttr, s.Revision, "a child must always be pinned")
	if err != nil {
		return Command{}, err
	}
	kind, err := ParseKind(string(s.Kind))
	if err != nil {
		return Command{}, err
	}

	argv := []string{
		"nix", "run", flakeref, "--",
		string(kind),
		// The pool cap lives in the daemon, so each child must be exactly
		// one Box. Both knobs are pinned so the guarantee does not depend
		// on which one a dispatch path honours.
		"--max-jobs", "1",
		"--max-parallel", "1",
	}
	argv = appendFeatureBranch(argv, s.FeatureBranch)
	argv, env := liftSignalCarrier(argv, s.Env)

	return Command{
		Argv: argv,
		// The markers are stripped before being appended, so an ambient
		// SPINDRIFT_REPORT_FD or PoolChildEnv (a nested daemon, a stray
		// export) cannot survive alongside the value set here.
		Env: append(withoutKeys(withoutKeys(env, s.Knobs), []string{reportFDEnv, PoolChildEnv}),
			fmt.Sprintf("%s=%d", reportFDEnv, ReportFD), PoolChildEnv+"=1"),
	}, nil
}

// withoutKeys filters env down to the entries whose key (the text before the
// first '=') is not in keys, preserving order. It always returns a non-nil
// slice: as exec.Cmd.Env, nil means "inherit the parent's environment",
// exactly the ambient-knob leak this prevents. An entry with no '=' always
// passes through.
func withoutKeys(env, keys []string) []string {
	strip := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		strip[k] = struct{}{}
	}
	out := make([]string, 0, len(env))
	for _, e := range env {
		key := e
		if i := strings.IndexByte(e, '='); i >= 0 {
			key = e[:i]
		}
		if _, stripped := strip[key]; stripped {
			continue
		}
		out = append(out, e)
	}
	return out
}

// appFlakeref validates repoPath/revision and pins attr's frag into a git
// flakeref, for ChildCommand and DoctorCommand. SelfCommand keeps its own
// frag handling: it rejects an empty attr rather than defaulting it.
// unpinnedMsg is each call site's wording for the empty-revision error.
func appFlakeref(repoPath, attr, revision, unpinnedMsg string) (string, error) {
	if err := validateRepoPathAndRevision(repoPath, revision, unpinnedMsg); err != nil {
		return "", err
	}
	// The default attr omits the fragment: nix rejects a bare trailing "#".
	// frag lands right after "#", so it gets repoPath's malformed-ref check.
	frag := strings.TrimPrefix(attr, ".#")
	if frag != "" && strings.ContainsAny(frag, "#?&") {
		return "", fmt.Errorf("app attr must not contain '#', '?', or '&', got %q", attr)
	}
	return pinnedFlakeref(repoPath, revision, frag), nil
}

// validateRepoPathAndRevision runs the checks appFlakeref and SelfCommand
// share: repoPath must be absolute and free of the chars that would open a
// second query string, fragment, or query param in the flakeref, and
// revision must be non-empty. The attr/frag checks diverge on empty-frag
// policy, so they stay with each caller.
func validateRepoPathAndRevision(repoPath, revision, unpinnedMsg string) error {
	if repoPath == "" || !path.IsAbs(repoPath) {
		return fmt.Errorf("repo path must be an absolute path, got %q", repoPath)
	}
	if revision == "" {
		return fmt.Errorf("revision must not be empty (%s)", unpinnedMsg)
	}
	if strings.ContainsAny(repoPath, "#?&") {
		return fmt.Errorf("repo path must not contain '#', '?', or '&', got %q", repoPath)
	}
	return nil
}

// pinnedFlakeref builds a git flakeref pinned to revision at repoPath, with
// frag (already stripped of its leading ".#") appended as the fragment when
// non-empty.
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
		return nil, fmt.Errorf("system must not be empty")
	}
	frag := strings.TrimPrefix(s.SelfAttr, ".#")
	// Unlike ChildCommand's AppAttr, an empty attr is an error:
	// "apps.<system>..program" is malformed, and nix eval has no default
	// app to fall back on.
	if frag == "" {
		return nil, fmt.Errorf("self attr must not be empty, got %q", s.SelfAttr)
	}
	if strings.ContainsAny(frag, "#?&") {
		return nil, fmt.Errorf("self attr must not contain '#', '?', or '&', got %q", s.SelfAttr)
	}

	// The full attribute path, not the bare attr: nix eval's fragment
	// resolution only tries packages/legacyPackages, never apps.
	frag = fmt.Sprintf("apps.%s.%s.program", s.System, frag)

	return []string{"nix", "eval", "--raw", pinnedFlakeref(s.RepoPath, s.Revision, frag)}, nil
}

// selfDrvApply is SelfDrvCommand's --apply expression: the program string's
// context holds exactly the wrapper derivation, which is what `nix build`
// can realise.
const selfDrvApply = `p: let c = builtins.attrNames (builtins.getContext p); in ` +
	`if builtins.length c == 1 then builtins.head c ` +
	`else throw "expected exactly one derivation in the program's context, got ${toString (builtins.length c)}"`

// SelfDrvCommand builds the argv that evaluates the .drv path behind the
// daemon's program at a pinned revision. `nix build` cannot take the program
// string itself (it errors "has context ... but the string is not the right
// placeholder"), so the build goes through the derivation, which the
// program string's context names.
func SelfDrvCommand(s SelfSpec) ([]string, error) {
	argv, err := SelfCommand(s)
	if err != nil {
		return nil, err
	}
	return append(argv, "--apply", selfDrvApply), nil
}

// SelfBuildCommand builds the argv that realises drvPath's default output,
// pins it with a GC root at outLink, and prints its store path.
func SelfBuildCommand(drvPath, outLink string) []string {
	return []string{"nix", "build", "--out-link", outLink, "--print-out-paths", drvPath + "^out"}
}

// SelfRootCommand builds the argv that pins the already-built storePath with
// a GC root at outLink.
func SelfRootCommand(storePath, outLink string) []string {
	return []string{"nix", "build", "--out-link", outLink, storePath}
}

// DoctorSpec is everything one pinned startup-preflight invocation needs.
type DoctorSpec struct {
	RepoPath string   // absolute path to the operator's checkout
	AppAttr  string   // DAEMON_APP, e.g. ".#" or ".#dogfood-bwrap"
	Revision string   // full git rev the preflight is pinned to
	Env      []string // the daemon's own environment, os.Environ() "KEY=VALUE" shape
	Knobs    []string // keys of the Launcher input document's settings map
	// FeatureBranch is the branch the preflight targets via --base-branch;
	// see appendFeatureBranch.
	FeatureBranch string
	// Flags are the kind flags doctor gets (e.g. --butler, --research) so
	// the preflight also runs those kinds' own checks; the caller picks them
	// from each kind's Preflight row.
	Flags []string
}

// DoctorCommand builds the Command for the daemon's startup preflight: the
// same pinned flakeref and env stripping as ChildCommand, invoking "doctor"
// with no --max-jobs/--max-parallel, since doctor dispatches nothing. Pure
// like ChildCommand.
//
// reportFDEnv is stripped and never re-added: the preflight emits no
// records, so it must see no ambient report fd either (issue #3627).
// PoolChildEnv likewise: the preflight is no pool child (issue #7342).
func DoctorCommand(s DoctorSpec) (Command, error) {
	flakeref, err := appFlakeref(s.RepoPath, s.AppAttr, s.Revision, "a preflight must always be pinned")
	if err != nil {
		return Command{}, err
	}
	argv := []string{"nix", "run", flakeref, "--", "doctor"}
	argv = append(argv, s.Flags...)
	argv = appendFeatureBranch(argv, s.FeatureBranch)
	return pinnedHostCommand(argv, s.Env, s.Knobs), nil
}

// LateMergeSweepSpec is everything one pinned late-merge sweep invocation
// needs; the fields mean what DoctorSpec's do.
type LateMergeSweepSpec struct {
	RepoPath      string
	AppAttr       string
	Revision      string
	Env           []string
	Knobs         []string
	FeatureBranch string
}

// LateMergeSweepCommand builds the Command for the daemon's own late-merge
// sweep (issue #7342): DoctorCommand's pinned flakeref and env stripping,
// invoking LateMergeSweepVerb. The sweep is no pool child and reports no
// records, so PoolChildEnv and reportFDEnv are stripped, never re-added.
func LateMergeSweepCommand(s LateMergeSweepSpec) (Command, error) {
	flakeref, err := appFlakeref(s.RepoPath, s.AppAttr, s.Revision, "a late-merge sweep must always be pinned")
	if err != nil {
		return Command{}, err
	}
	argv := appendFeatureBranch([]string{"nix", "run", flakeref, "--", LateMergeSweepVerb}, s.FeatureBranch)
	return pinnedHostCommand(argv, s.Env, s.Knobs), nil
}

// pinnedHostCommand is the shared tail of the host-side pinned commands
// (doctor, late-merge sweep): the signal carrier lifted onto argv, and an env
// stripped of the Knobs plus the pool-child keys, which a command that is no
// pool child and reports no records must never inherit.
func pinnedHostCommand(argv, env, knobs []string) Command {
	argv, env = liftSignalCarrier(argv, env)
	return Command{Argv: argv, Env: withoutKeys(withoutKeys(env, knobs), []string{reportFDEnv, PoolChildEnv})}
}
