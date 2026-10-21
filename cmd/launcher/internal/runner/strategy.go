package runner

import (
	"errors"
	"fmt"
	"sort"
)

// KnobLookup resolves one knob's env name to its already-resolved value (after
// any _CMD resolution) and whether it is set; os.LookupEnv has this shape. A
// family hands it to its generated ReadKnobs, so its own knobs never widen
// Config (ADR 0068 decision 3).
type KnobLookup func(env string) (value string, ok bool)

// Strategy is one runner family's entry in the registry (ADR 0069 decision 2;
// CONTEXT.md "Runner Strategy"). Name is the family's RUNNER_KIND string.
type Strategy struct {
	Name string
	// New constructs the Runner for the run command; a malformed family knob
	// read through l is an error.
	New func(cfg Config, l KnobLookup, pwd string) (Runner, error)
	// NewBuild constructs the Runner for the build command.
	NewBuild func(cfg Config, l KnobLookup, pwd string) (Runner, error)
	// Prerequisites declares the family's host checks in report order. Tiers and
	// remedies depend on cfg and the family's knobs, read through l; nil means
	// none.
	Prerequisites func(cfg Config, l KnobLookup) []Prerequisite
	// PrepareHotSwap prepares a realized store-closure (a /nix/store path) before
	// a hot-swap binds it as the new agent generation; core calls it only for a
	// nixInBox Consumer. nil means the family needs no preparation (ADR 0069
	// decision 2).
	PrepareHotSwap func(pwd, closure string) error
	// RawNetworkKnobs returns the env names of the family's raw network knobs
	// set in l, read through the family's own reader; core refuses a non-open
	// NETWORK_MODE beside any of them. nil means the family has none.
	RawNetworkKnobs func(l KnobLookup) ([]string, error)
}

// Launch says where the launch path enforces a Required prerequisite. Doctor
// reports every prerequisite whatever its Launch.
type Launch int

const (
	// DoctorOnly prerequisites are checked by doctor alone.
	DoctorOnly Launch = iota
	// LaunchWithConfig prerequisites run with the launcher's config checks,
	// ahead of every launch gate.
	LaunchWithConfig
	// LaunchGate prerequisites run among the launch gates, ahead of the
	// network-bound ones.
	LaunchGate
)

// Prerequisite is one Runner prerequisite (CONTEXT.md; ADR 0069 decision 4): a
// named host check a family declares on its Strategy. Runner core cannot hold a
// doctor.Check (doctor imports runner), so doctor adapts this type.
type Prerequisite struct {
	Name string
	// Launch says where the launch path enforces this check when Required.
	Launch Launch
	// LaunchOrder orders a stage's checks at launch, ascending, ties in
	// declaration order. Launch stops at the first failure, so this decides
	// which error a host failing several checks sees, independent of doctor's
	// declaration-order report.
	LaunchOrder int
	// Required is the check's tier for this Config; false is Advisory.
	Required bool
	Remedy   string
	// Probe returns a non-nil error on failure. The launch path returns that
	// error verbatim, so a launch-enforced Probe must not wrap it.
	Probe func() (any, error)
	// SuccessMsg optionally overrides doctor's "ok: <Name>" line.
	SuccessMsg func(output any) string
}

// LaunchGates returns the Required prerequisites of ps whose Launch is stage,
// stably sorted by LaunchOrder. ps is not mutated.
func LaunchGates(ps []Prerequisite, stage Launch) []Prerequisite {
	var out []Prerequisite
	for _, p := range ps {
		if p.Launch == stage && p.Required {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LaunchOrder < out[j].LaunchOrder })
	return out
}

// ErrDegraded marks a Prerequisite Probe error as indeterminate: the probe
// could not determine the condition it checks, as distinct from affirmatively
// detecting it. Doctor treats a Required failure wrapping it as non-blocking
// and still prints it, with its Remedy, as an advisory line.
var ErrDegraded = errors.New("check degraded: probe could not determine result")

// PrerequisitesFor returns the family's prerequisites for cfg and l, or nil when it
// declares none.
func (s Strategy) PrerequisitesFor(cfg Config, l KnobLookup) []Prerequisite {
	if s.Prerequisites == nil {
		return nil
	}
	return s.Prerequisites(cfg, l)
}

var strategies = map[string]Strategy{}

// Register adds s to the registry, keyed on s.Name. It panics on a duplicate
// name, which is a programming error rather than a runtime condition.
func Register(s Strategy) {
	if _, exists := strategies[s.Name]; exists {
		panic("runner: duplicate registration for " + s.Name)
	}
	strategies[s.Name] = s
}

// defaultKind is the runner family an empty RUNNER_KIND selects.
// runner-family-guard: unset RUNNER_KIND means OCI; Lookup still resolves it.
const defaultKind = KindOCI

// orDefault resolves an empty runner kind to defaultKind.
func orDefault(kind string) string {
	if kind == "" {
		return defaultKind
	}
	return kind
}

// Lookup returns the Strategy registered under kind. An empty kind selects
// KindOCI: an unset RUNNER_KIND means OCI.
func Lookup(kind string) (Strategy, error) {
	kind = orDefault(kind)
	s, ok := strategies[kind]
	if !ok {
		return Strategy{}, fmt.Errorf("unknown RUNNER_KIND %q; registered runner families: %v", kind, Names())
	}
	return s, nil
}

// Names lists the registered runner families, sorted.
func Names() []string {
	names := make([]string, 0, len(strategies))
	for n := range strategies {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
