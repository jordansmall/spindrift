package chore

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
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
	Every            time.Duration // BUTLER_EVERY override, else the catalog default (builtinEvery), else the bare default, else DefaultEvery.
	PromotionClasses []string      // BUTLER_CHORE_CLASSES allow-list; nil when the Chore has no entry.
	PatchClasses     []string      // BUTLER_PATCH_CLASSES allow-list; nil unless MaxPatchesPerDay > 0 and the Chore has an entry (ADR 0057).
	Records          bool          // Swept from the Dispatch Records store, not the tree; never promotes or patches (ADR 0062).
	FindingLabel     string        // Provenance label every finding of this Chore wears (the catalog's findingLabel); empty for a Chore with none.
	ClassList        []string      // Closed class list the Box classifies from (issue #4766): the catalog's plus any extra PromotionClasses; nil for a Consumer-declared Chore.
}

// Knobs is the raw butler settings (ADR 0056, ADR 0057), unparsed but for
// MaxPatchesPerDay.
type Knobs struct {
	Chores, Every, Classes, PatchClasses string
	// MaxPatchesPerDay is BUTLER_MAX_PATCHES_PER_DAY. 0 (the default) turns
	// the patch rung off: PatchClasses is never read.
	MaxPatchesPerDay int
}

// Load resolves Knobs into one Chore per enabled name, in BUTLER_CHORES
// order. With no Chore enabled it returns (nil, nil) without parsing the other
// knobs, which then configure nothing, so a leftover or malformed value is no
// startup error; with MaxPatchesPerDay == 0 it likewise skips
// BUTLER_PATCH_CLASSES, whose default names docs-drift and would otherwise
// fail as not enabled for every Consumer off the patch rung. Otherwise every
// problem is joined, each prefixed with its knob: a grammar reports its first
// fault, a cross-check every offender. A BUTLER_CHORE_CLASSES entry may name a
// disabled code-scanning built-in (the schema default names each), but never
// the records-scoped tuning Chore. A BUTLER_PATCH_CLASSES entry must name an
// enabled Chore, each class on its BUTLER_CHORE_CLASSES allow-list.
func Load(k Knobs) ([]Chore, error) {
	names := choreNames(k.Chores)
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
		// Re-split for token order (a map has none); it parsed, so this can't fail.
		for _, entry := range strings.Fields(k.Classes) {
			name, _, _ := strings.Cut(entry, "=")
			if slices.Contains(builtinRecordsScoped, name) {
				errs = append(errs, fmt.Errorf("BUTLER_CHORE_CLASSES: chore %q is records-scoped and never promotes (ADR 0062)", name))
				continue
			}
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
			for _, entry := range strings.Fields(k.PatchClasses) {
				name, classesPart, _ := strings.Cut(entry, "=")
				if slices.Contains(builtinRecordsScoped, name) {
					errs = append(errs, fmt.Errorf("BUTLER_PATCH_CLASSES: chore %q is records-scoped and never patches (ADR 0062)", name))
					continue
				}
				if enabled[name] == 0 {
					errs = append(errs, fmt.Errorf("BUTLER_PATCH_CLASSES: chore %q is not enabled (BUTLER_CHORES=%q)", name, k.Chores))
					continue
				}
				if classesErr != nil {
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
			Records:          slices.Contains(builtinRecordsScoped, name),
			FindingLabel:     FindingLabel(name),
			ClassList:        classList(name, promotionClassesMap[name]),
		}
	}
	return result, nil
}

// FindingLabel is the provenance label a built-in Chore's findings wear, or ""
// for a Chore with none or an unknown name. Unlike Load it cannot fail, so a
// caller that must not lose the label to an unrelated knob fault can use it.
func FindingLabel(name string) string { return builtinFindingLabels[name] }

// MinRecordsKnob is the env name of the knob holding a records-scoped built-in
// Chore's minimum new-Record count, or "" for a Chore with none.
func MinRecordsKnob(name string) string { return builtinMinRecordsKnobs[name] }

// MinRecords resolves a records-scoped built-in Chore's minimum new-Record
// count through lookup: a nil lookup, an unset or an empty value is the row
// default, anything else must be an integer. It applies the same rule as the
// row's generated tuning reader. A Chore whose row names no knob resolves to 0.
func MinRecords(name string, lookup func(env string) (string, bool)) (int, error) {
	env := MinRecordsKnob(name)
	if env == "" {
		return 0, nil
	}
	var v string
	var ok bool
	if lookup != nil {
		v, ok = lookup(env)
	}
	if !ok || v == "" {
		return builtinMinRecordsDefaults[name], nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not an integer", env, v)
	}
	return n, nil
}

// FindingLabels is the deduplicated FindingLabel of each BUTLER_CHORES entry,
// tokenised the way Load tokenises it, so a caller never re-parses the list.
func FindingLabels(chores string) []string {
	var labels []string
	for _, name := range choreNames(chores) {
		if l := FindingLabel(name); l != "" && !slices.Contains(labels, l) {
			labels = append(labels, l)
		}
	}
	return labels
}

// Row is one built-in Chore catalog row's host-facing facts.
type Row struct {
	Name         string
	FindingLabel string // "" for a Chore with none
	MergeByHand  bool   // the host never merges a PR closing one of its findings
	Records      bool   // scopeSource = "records": it sweeps the Dispatch Records store
}

// Rows lists the built-in catalog in builtinChores order.
func Rows() []Row {
	rows := make([]Row, len(builtinChores))
	for i, name := range builtinChores {
		rows[i] = Row{
			Name:         name,
			FindingLabel: FindingLabel(name),
			MergeByHand:  slices.Contains(builtinMergeByHand, name),
			Records:      slices.Contains(builtinRecordsScoped, name),
		}
	}
	return rows
}

func choreNames(chores string) []string { return strings.Fields(chores) }

// everyConfig is BUTLER_EVERY parsed: a bare default interval plus
// per-Chore overrides. With no bare token, the default is DefaultEvery; a
// bare "0" token, unlike an absent one, still means "no interval".
type everyConfig struct {
	dflt      time.Duration
	overrides map[string]time.Duration
}

// For returns name's interval: its override, else its catalog default
// (builtinEvery), else the bare default. The catalog default outranks the bare
// token: a Chore the catalog gives its own cadence must not be dragged onto
// the sweep-wide one.
func (c everyConfig) For(name string) time.Duration {
	if d, ok := c.overrides[name]; ok {
		return d
	}
	if d, ok := catalogEvery[name]; ok {
		return d
	}
	return c.dflt
}

// catalogEvery is builtinEvery parsed. A malformed generated string is a
// programming error, so it panics at init.
var catalogEvery = func() map[string]time.Duration {
	out := make(map[string]time.Duration, len(builtinEvery))
	for name, s := range builtinEvery {
		d, err := parseNonNegativeDuration(s)
		if err != nil {
			panic(fmt.Sprintf("chore: builtinEvery[%q]: %v", name, err))
		}
		out[name] = d
	}
	return out
}()

// parseEvery parses BUTLER_EVERY's grammar (ADR 0056): space-separated
// tokens, each a bare Go duration (the default interval) or a per-Chore
// "<chore>=<duration>" override, e.g. "6h docs-drift=168h".
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
