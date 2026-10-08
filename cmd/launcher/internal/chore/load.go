package chore

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/promptassembly"
)

// DefaultEvery is the interval a Chore gets when BUTLER_EVERY carries no
// bare default token (ADR 0056).
const DefaultEvery = 6 * time.Hour

// Chore is one enabled Chore resolved from BUTLER_CHORES, BUTLER_EVERY,
// BUTLER_CHORE_CLASSES and (when the patch rung is on) BUTLER_PATCH_CLASSES
// (ADR 0056, ADR 0057).
type Chore struct {
	Name             string
	Every            time.Duration // BUTLER_EVERY override, else its bare default, else DefaultEvery.
	PromotionClasses []string      // BUTLER_CHORE_CLASSES allow-list; nil when the Chore has no entry.
	PatchClasses     []string      // BUTLER_PATCH_CLASSES allow-list; nil unless MaxPatchesPerDay > 0 and the Chore has an entry (ADR 0057).
	ClassList        []string      // Closed class list the Box classifies from (issue #4766): the catalog's plus any extra PromotionClasses; nil for a Consumer-declared Chore.
}

// Knobs is the raw butler settings (ADR 0056, ADR 0057): BUTLER_CHORES,
// BUTLER_EVERY, BUTLER_CHORE_CLASSES and BUTLER_PATCH_CLASSES, unparsed, plus
// BUTLER_MAX_PATCHES_PER_DAY already parsed to an int (it gates whether
// PatchClasses is read at all).
type Knobs struct {
	Chores, Every, Classes, PatchClasses string
	// MaxPatchesPerDay is BUTLER_MAX_PATCHES_PER_DAY. 0 (the default) means
	// the patch rung is off: Load never parses or cross-checks PatchClasses,
	// and every resulting Chore.PatchClasses is nil regardless of its value.
	MaxPatchesPerDay int
}

// Load resolves Knobs into one Chore per enabled name, in BUTLER_CHORES
// order. When BUTLER_CHORES enables no Chore, Load returns (nil, nil)
// immediately without parsing or cross-checking BUTLER_EVERY,
// BUTLER_CHORE_CLASSES or BUTLER_PATCH_CLASSES: with no Chore enabled,
// those knobs configure nothing, so a leftover or malformed value in any of
// them is not a startup error. When MaxPatchesPerDay == 0, BUTLER_PATCH_CLASSES
// is likewise never parsed or cross-checked, for the same reason (its
// default names docs-drift, which would otherwise be rejected as
// not-enabled for every Consumer that never turns the patch rung on).
// Otherwise every problem is returned together via errors.Join, each
// prefixed with its knob. A knob's grammar reports only its first fault,
// since its parser stops there; the cross-checks report every offender,
// once per offender. A BUTLER_CHORE_CLASSES entry may name a built-in
// Chore that is not enabled: the schema default lists every built-in, so
// such an entry is inert rather than a typo. A BUTLER_PATCH_CLASSES entry
// has no such exemption: it must name an enabled Chore, and each of its
// classes must be on that Chore's resolved BUTLER_CHORE_CLASSES allow-list
// (skipped when BUTLER_CHORE_CLASSES itself failed to parse).
func Load(k Knobs) ([]Chore, error) {
	names := strings.Fields(k.Chores)
	if len(names) == 0 {
		return nil, nil
	}

	var errs []error

	enabled := make(map[string]int, len(names))
	for _, name := range names {
		switch valid := promptassembly.ValidChoreName(name); {
		case !valid && enabled[name] == 0:
			errs = append(errs, fmt.Errorf("BUTLER_CHORES: chore %q: invalid name format: %s", name, promptassembly.ChoreNameRule))
		case valid && enabled[name] == 1:
			// ledger.DayTotalsAll sums per name, so a repeat double-counts the daily budgets.
			errs = append(errs, fmt.Errorf("BUTLER_CHORES: duplicate chore %q", name))
		}
		enabled[name]++
	}

	everyCfg, everyErr := parseEvery(k.Every)
	if everyErr != nil {
		errs = append(errs, fmt.Errorf("BUTLER_EVERY: %w", everyErr))
	} else {
		overrideNames := make([]string, 0, len(everyCfg.overrides))
		for name := range everyCfg.overrides {
			overrideNames = append(overrideNames, name)
		}
		slices.Sort(overrideNames)
		for _, name := range overrideNames {
			if enabled[name] == 0 {
				errs = append(errs, fmt.Errorf("BUTLER_EVERY: override for chore %q, which is not enabled (BUTLER_CHORES=%q)", name, k.Chores))
			}
		}
	}

	promotionClassesMap, classesErr := parseClasses(k.Classes)
	if classesErr != nil {
		errs = append(errs, fmt.Errorf("BUTLER_CHORE_CLASSES: %w", classesErr))
	} else {
		// classesErr == nil means every entry in classes already parsed
		// cleanly, so re-splitting it here for token order (a map has none)
		// can't fail.
		for _, entry := range strings.Fields(k.Classes) {
			name, _, _ := strings.Cut(entry, "=")
			if enabled[name] > 0 || slices.Contains(builtinChores, name) {
				continue
			}
			errs = append(errs, fmt.Errorf("BUTLER_CHORE_CLASSES: chore %q is not enabled and not a built-in chore (BUTLER_CHORES=%q)", name, k.Chores))
		}
	}

	var patchClassesMap map[string][]string
	if k.MaxPatchesPerDay > 0 {
		var patchErr error
		patchClassesMap, patchErr = parseClasses(k.PatchClasses)
		if patchErr != nil {
			errs = append(errs, fmt.Errorf("BUTLER_PATCH_CLASSES: %w", patchErr))
		} else {
			// patchErr == nil means every entry already parsed cleanly, so
			// re-splitting it here for token order (a map has none) can't
			// fail.
			for _, entry := range strings.Fields(k.PatchClasses) {
				name, classesPart, _ := strings.Cut(entry, "=")
				if enabled[name] == 0 {
					errs = append(errs, fmt.Errorf("BUTLER_PATCH_CLASSES: chore %q is not enabled (BUTLER_CHORES=%q)", name, k.Chores))
					continue
				}
				if classesErr != nil {
					// BUTLER_CHORE_CLASSES itself failed to parse: nothing to
					// check the subset against.
					continue
				}
				for _, class := range strings.Split(classesPart, ",") {
					if !slices.Contains(promotionClassesMap[name], class) {
						errs = append(errs, fmt.Errorf("BUTLER_PATCH_CLASSES: chore %q: class %q is not on its BUTLER_CHORE_CLASSES allow-list", name, class))
					}
				}
			}
		}
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	result := make([]Chore, len(names))
	for i, name := range names {
		result[i] = Chore{
			Name:             name,
			Every:            everyCfg.For(name),
			PromotionClasses: promotionClassesMap[name],
			PatchClasses:     patchClassesMap[name],
			ClassList:        classList(name, promotionClassesMap[name]),
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

// parseEvery parses BUTLER_EVERY's grammar (ADR 0056): space-separated
// tokens, each either a bare Go time.ParseDuration string (the default
// interval for every enabled Chore not otherwise overridden) or
// "<chore>=<duration>" (a per-Chore override), e.g. "6h docs-drift=168h". A
// value with no bare token falls back to DefaultEvery. Rejects an
// unparseable or negative duration, more than one bare default token, a
// duplicate override for the same Chore, and an empty chore name in a
// "=<duration>" token.
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

// classList is name's catalog list with any configured class missing from it
// appended in configured order, so a Consumer-added promotion class stays
// choosable. nil when name has no catalog entry. Always a fresh slice.
func classList(name string, configured []string) []string {
	base, ok := builtinClassLists[name]
	if !ok {
		return nil
	}
	out := slices.Clone(base)
	for _, c := range configured {
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}
