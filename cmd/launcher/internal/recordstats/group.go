package recordstats

import (
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/passmachine"
)

// Group is the Records (or, for a pass-level key, the slices of Records)
// sharing one value of a grouping key.
type Group struct {
	Key     string
	Records []dispatchrecord.Record
}

// GroupRecords splits claim-time-sorted Records into groups by a Record-level
// key. Groups keep first-appearance order, (none) last.
func GroupRecords(records []dispatchrecord.Record, key func(dispatchrecord.Record) string) []Group {
	g := grouper{index: map[string]int{}}
	for _, r := range records {
		g.add(key(r), r)
	}
	return g.finish()
}

// GroupPasses splits claim-time-sorted Records into groups by a pass-level
// key. A Record is copied into every group one of its passes belongs to,
// carrying only those passes; a Record with no passes lands whole in the
// (none) group so it never vanishes. Groups keep first-appearance order, (none)
// last.
func GroupPasses(records []dispatchrecord.Record, key func(dispatchrecord.Pass) string) []Group {
	g := grouper{index: map[string]int{}}
	for _, r := range records {
		if len(r.Passes) == 0 {
			g.add(None, r)
			continue
		}
		var keys []string
		byKey := map[string][]dispatchrecord.Pass{}
		for _, p := range r.Passes {
			k := key(p)
			if _, ok := byKey[k]; !ok {
				keys = append(keys, k)
			}
			byKey[k] = append(byKey[k], p)
		}
		for _, k := range keys {
			sub := r
			sub.Passes = byKey[k]
			g.add(k, sub)
		}
	}
	return g.finish()
}

type grouper struct {
	groups []Group
	index  map[string]int
}

func (g *grouper) add(key string, r dispatchrecord.Record) {
	i, ok := g.index[key]
	if !ok {
		i = len(g.groups)
		g.index[key] = i
		g.groups = append(g.groups, Group{Key: key})
	}
	g.groups[i].Records = append(g.groups[i].Records, r)
}

func (g *grouper) finish() []Group {
	slices.SortStableFunc(g.groups, noneLast)
	return g.groups
}

func noneLast(a, b Group) int {
	if an, bn := a.Key == None, b.Key == None; an != bn {
		if an {
			return 1
		}
		return -1
	}
	return 0
}

// SortRoleGroups orders groups keyed by role in pipeline order, (none) last.
func SortRoleGroups(groups []Group) {
	slices.SortStableFunc(groups, func(a, b Group) int {
		if c := noneLast(a, b); c != 0 {
			return c
		}
		if ra, rb := RoleRank(a.Key), RoleRank(b.Key); ra != rb {
			return ra - rb
		}
		return strings.Compare(a.Key, b.Key)
	})
}

// NoneIfEmpty returns s, or None when s is empty.
func NoneIfEmpty(s string) string {
	if s == "" {
		return None
	}
	return s
}

// RoleKey is the pass-level key of a pass's role.
func RoleKey(p dispatchrecord.Pass) string { return NoneIfEmpty(p.Role) }

// ModelKey is the pass-level key of the models a pass ran, sorted and joined
// with "+".
func ModelKey(p dispatchrecord.Pass) string {
	models := slices.Clone(p.Models)
	slices.Sort(models)
	return NoneIfEmpty(strings.Join(models, "+"))
}

// RevisionKey is the Record-level key of the revision a Record ran at.
func RevisionKey(r dispatchrecord.Record) string { return NoneIfEmpty(r.Revision) }

// PromptKey is the Record-level key of the hash of one role's prompt.
func PromptKey(role string) func(dispatchrecord.Record) string {
	return func(r dispatchrecord.Record) string { return NoneIfEmpty(r.PromptHashes[role]) }
}

// PromptRoles lists the names a prompt_hashes op can key by, matching
// promptassembly's pass names: every pass shape, plus each single-pass
// dispatch kind that renders from one base prompt.
func PromptRoles() []string {
	var roles []string
	for _, k := range passmachine.Kinds {
		roles = append(roles, k.ManifestKind())
	}
	for _, d := range dispatchkind.All {
		if d.Prompts.Base != "" {
			roles = append(roles, d.Name)
		}
	}
	return roles
}

// RevertedPercent is the percentage of filled Records whose merge was
// reverted, and how many are filled; zero filled means no data. An unfilled
// Record is not yet known to stand, so it never counts as zero.
func RevertedPercent(records []dispatchrecord.Record) (pct float64, filled int) {
	reverted := 0
	for _, r := range records {
		if r.Reverted == nil {
			continue
		}
		filled++
		if *r.Reverted {
			reverted++
		}
	}
	if filled == 0 {
		return 0, 0
	}
	return 100 * float64(reverted) / float64(filled), filled
}

// MeanChurnPercent is the mean 14-day churn of the Records that carry one, and
// how many do; zero filled means no data. A Record without one is unfilled or
// its lines cannot be told, so it is skipped rather than averaged in as zero.
func MeanChurnPercent(records []dispatchrecord.Record) (pct float64, filled int) {
	sum := 0.0
	for _, r := range records {
		if r.Churn14d == nil {
			continue
		}
		filled++
		sum += *r.Churn14d
	}
	if filled == 0 {
		return 0, 0
	}
	return 100 * sum / float64(filled), filled
}
