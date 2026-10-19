package runner

import (
	"fmt"
	"sort"
)

// Strategy is one runner family's entry in the registry (ADR 0069 decision 2;
// CONTEXT.md "Runner Strategy"). Name is the family's RUNNER_KIND string.
type Strategy struct {
	Name string
	// New constructs the Runner for the run command.
	New func(cfg Config, pwd string) Runner
	// NewBuild constructs the Runner for the build command.
	NewBuild func(cfg Config, pwd string) Runner
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
