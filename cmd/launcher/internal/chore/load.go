package chore

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/promptassembly"
)

// DefaultEvery is the interval a Chore gets when BUTLER_EVERY carries no
// bare default token (ADR 0056).
const DefaultEvery = 6 * time.Hour

// Chore is one enabled Chore resolved from BUTLER_CHORES, BUTLER_EVERY and
// BUTLER_CHORE_CLASSES (ADR 0056).
type Chore struct {
	Name    string
	Every   time.Duration // BUTLER_EVERY override, else its bare default, else DefaultEvery.
	Classes []string      // BUTLER_CHORE_CLASSES allow-list; nil when the Chore has no entry.
}

// Load resolves BUTLER_CHORES, BUTLER_EVERY and BUTLER_CHORE_CLASSES into
// one Chore per enabled name, in BUTLER_CHORES order; an empty BUTLER_CHORES
// yields none, and whether that is an error (ErrNoChores) is the caller's
// call. Every problem is returned together via errors.Join, each prefixed
// with its knob. A knob's grammar reports only its first fault, since its
// parser stops there; the cross-checks report every offender. A
// BUTLER_CHORE_CLASSES entry may name a built-in Chore that is not enabled:
// the schema default lists every built-in, so such an entry is inert rather
// than a typo.
func Load(chores, every, classes string) ([]Chore, error) {
	var errs []error

	names := Chores(chores)
	enabled := make(map[string]bool, len(names))
	for _, name := range names {
		if !promptassembly.ValidChoreName(name) {
			errs = append(errs, fmt.Errorf("BUTLER_CHORES: chore %q: invalid name format: %s", name, promptassembly.ChoreNameRule))
		}
		enabled[name] = true
	}

	everyCfg, everyErr := parseEvery(every)
	if everyErr != nil {
		errs = append(errs, fmt.Errorf("BUTLER_EVERY: %w", everyErr))
	} else {
		overrideNames := make([]string, 0, len(everyCfg.overrides))
		for name := range everyCfg.overrides {
			overrideNames = append(overrideNames, name)
		}
		sort.Strings(overrideNames)
		for _, name := range overrideNames {
			if !enabled[name] {
				errs = append(errs, fmt.Errorf("BUTLER_EVERY: override for chore %q, which is not enabled (BUTLER_CHORES=%q)", name, chores))
			}
		}
	}

	classesMap, classesErr := ParseClasses(classes)
	if classesErr != nil {
		errs = append(errs, fmt.Errorf("BUTLER_CHORE_CLASSES: %w", classesErr))
	} else {
		// classesErr == nil means every entry in classes already parsed
		// cleanly, so re-splitting it here for token order (a map has none)
		// can't fail.
		for _, entry := range strings.Fields(classes) {
			name, _, _ := strings.Cut(entry, "=")
			if enabled[name] || slices.Contains(Builtins, name) {
				continue
			}
			errs = append(errs, fmt.Errorf("BUTLER_CHORE_CLASSES: chore %q is not enabled and not a built-in chore (BUTLER_CHORES=%q)", name, chores))
		}
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	result := make([]Chore, len(names))
	for i, name := range names {
		result[i] = Chore{
			Name:    name,
			Every:   everyCfg.For(name),
			Classes: classesMap[name],
		}
	}
	return result, nil
}

// everyConfig is BUTLER_EVERY parsed: a bare default interval plus
// per-Chore overrides. With no bare token, the default is DefaultEvery; a
// bare "0" token, unlike an absent one, still means "no interval".
type everyConfig struct {
	dflt      time.Duration
	overrides map[string]time.Duration
}

// For returns name's interval: its override, else the bare default.
func (c everyConfig) For(name string) time.Duration {
	if d, ok := c.overrides[name]; ok {
		return d
	}
	return c.dflt
}

// parseEvery parses BUTLER_EVERY's grammar (ADR 0056): space-separated tokens, each either a
// bare Go time.ParseDuration string (the default interval for every enabled
// Chore not otherwise overridden) or "<chore>=<duration>" (a per-Chore
// override), e.g. "6h docs-drift=168h". A value with no bare token falls
// back to DefaultEvery. Rejects an unparseable or negative duration, more
// than one bare default token, a duplicate override for the same Chore, and
// an empty chore name in a "=<duration>" token.
func parseEvery(value string) (everyConfig, error) {
	cfg := everyConfig{overrides: make(map[string]time.Duration)}
	haveDefault := false
	for _, tok := range strings.Fields(value) {
		name, durStr, isOverride := strings.Cut(tok, "=")
		if isOverride {
			if name == "" {
				return everyConfig{}, fmt.Errorf("empty chore name in %q", tok)
			}
			if _, exists := cfg.overrides[name]; exists {
				return everyConfig{}, fmt.Errorf("duplicate override for chore %q", name)
			}
			d, err := parseNonNegativeDuration(durStr)
			if err != nil {
				return everyConfig{}, fmt.Errorf("chore %q: %w", name, err)
			}
			cfg.overrides[name] = d
			continue
		}
		if haveDefault {
			return everyConfig{}, fmt.Errorf("more than one bare default token in %q", value)
		}
		d, err := parseNonNegativeDuration(tok)
		if err != nil {
			return everyConfig{}, err
		}
		cfg.dflt = d
		haveDefault = true
	}
	if !haveDefault {
		cfg.dflt = DefaultEvery
	}
	return cfg, nil
}

// parseNonNegativeDuration parses s as a Go duration, rejecting a negative
// result; parseEvery's shared validation for both its bare-default and
// per-Chore-override tokens.
func parseNonNegativeDuration(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("negative duration %q", s)
	}
	return d, nil
}
