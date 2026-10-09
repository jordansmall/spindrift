package main

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/passmachine"
	"spindrift.dev/launcher/internal/recordstats"
)

type statsDim string

const (
	statsByRole     statsDim = "role"
	statsByRevision statsDim = "revision"
	statsByModel    statsDim = "model"
	statsByPrompt   statsDim = "prompt"
	statsByKnob     statsDim = "knob"
)

// statsBy is a parsed --by value. The zero value is no --by. arg is the prompt
// role or knob name of the prompt and knob dimensions, empty otherwise.
type statsBy struct {
	dim statsDim
	arg string
}

// String is the --by spelling, used for the "<by> = <key>" group header.
func (b statsBy) String() string {
	if b.arg == "" {
		return string(b.dim)
	}
	return string(b.dim) + ":" + b.arg
}

// passLevel reports whether the dimension groups passes rather than Records.
func (b statsBy) passLevel() bool {
	return b.dim == statsByRole || b.dim == statsByModel
}

// statsGroup is the Records (or, for a pass-level dimension, the slices of
// Records) sharing one value of the --by dimension.
type statsGroup struct {
	key     string
	records []dispatchrecord.Record
}

// statsPromptRoles lists the names a prompt_hashes op can key by, matching
// promptassembly's pass names: every pass shape, plus each single-pass
// dispatch kind that renders from one base prompt.
func statsPromptRoles() []string {
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

// statsKnobNames lists the knobs a dispatch_start stamp records: every schema
// knob except the secret ones.
func statsKnobNames() (names, secret []string) {
	for _, s := range secretKnobs {
		secret = append(secret, s.env)
	}
	for _, e := range schemaFlags {
		if !slices.Contains(secret, e.env) {
			names = append(names, e.env)
		}
	}
	return names, secret
}

// parseStatsBy checks a --by value against the dimensions stats can group by.
func parseStatsBy(by string) (statsBy, error) {
	switch {
	case by == string(statsByRole), by == string(statsByRevision), by == string(statsByModel):
		return statsBy{dim: statsDim(by)}, nil
	case strings.HasPrefix(by, string(statsByPrompt)+":"):
		role := strings.TrimPrefix(by, string(statsByPrompt)+":")
		if roles := statsPromptRoles(); !slices.Contains(roles, role) {
			return statsBy{}, fmt.Errorf("invalid --by %q: unknown prompt role %q, want one of %s", by, role, strings.Join(roles, ", "))
		}
		return statsBy{dim: statsByPrompt, arg: role}, nil
	case strings.HasPrefix(by, string(statsByKnob)+":"):
		name := strings.TrimPrefix(by, string(statsByKnob)+":")
		known, secret := statsKnobNames()
		switch {
		case name == "":
			return statsBy{}, fmt.Errorf("invalid --by %q: knob name is empty", by)
		case slices.Contains(secret, name):
			return statsBy{}, fmt.Errorf("invalid --by %q: knob %s is secret, never recorded", by, name)
		case !slices.Contains(known, name):
			return statsBy{}, fmt.Errorf("invalid --by %q: unknown knob %q", by, name)
		}
		return statsBy{dim: statsByKnob, arg: name}, nil
	}
	return statsBy{}, fmt.Errorf("invalid --by %q: want role, revision, model, prompt:<role> or knob:<NAME>", by)
}

// statsPassKey is the group key of a pass for a pass-level dimension.
func statsPassKey(by statsBy, p dispatchrecord.Pass) string {
	switch by.dim {
	case statsByRole:
		return noneIfEmpty(p.Role)
	case statsByModel:
		models := slices.Clone(p.Models)
		slices.Sort(models)
		return noneIfEmpty(strings.Join(models, "+"))
	}
	panic("stats: no pass-level key for --by " + by.String())
}

func noneIfEmpty(s string) string {
	if s == "" {
		return recordstats.None
	}
	return s
}

// statsRecordKey is the group key of a Record-level dimension.
func statsRecordKey(by statsBy, r dispatchrecord.Record) string {
	switch by.dim {
	case statsByRevision:
		return noneIfEmpty(r.Revision)
	case statsByPrompt:
		return noneIfEmpty(r.PromptHashes[by.arg])
	case statsByKnob:
		v, ok := r.Knobs[by.arg]
		switch {
		case !ok:
			return recordstats.None
		case v == "":
			return statsEmpty
		}
		return v
	}
	panic("stats: no Record-level key for --by " + by.String())
}

// groupStats splits claim-time-sorted Records into ordered groups by a --by
// dimension. A pass-level dimension (role, model) copies a Record into every
// group one of its passes belongs to, carrying only those passes; a Record
// with no passes lands whole in the (none) group so it never vanishes. Groups
// keep first-appearance order (the pipeline order for role), (none) last.
func groupStats(records []dispatchrecord.Record, by statsBy) []statsGroup {
	var groups []statsGroup
	index := map[string]int{}
	add := func(key string, r dispatchrecord.Record) {
		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i
			groups = append(groups, statsGroup{key: key})
		}
		groups[i].records = append(groups[i].records, r)
	}
	passLevel := by.passLevel()
	for _, r := range records {
		switch {
		case !passLevel:
			add(statsRecordKey(by, r), r)
		case len(r.Passes) == 0:
			add(recordstats.None, r)
		default:
			var keys []string
			byKey := map[string][]dispatchrecord.Pass{}
			for _, p := range r.Passes {
				k := statsPassKey(by, p)
				if _, ok := byKey[k]; !ok {
					keys = append(keys, k)
				}
				byKey[k] = append(byKey[k], p)
			}
			for _, k := range keys {
				sub := r
				sub.Passes = byKey[k]
				add(k, sub)
			}
		}
	}
	slices.SortStableFunc(groups, func(a, b statsGroup) int {
		if an, bn := a.key == recordstats.None, b.key == recordstats.None; an != bn {
			if an {
				return 1
			}
			return -1
		}
		if by.dim == statsByRole {
			if ra, rb := recordstats.RoleRank(a.key), recordstats.RoleRank(b.key); ra != rb {
				return ra - rb
			}
			return strings.Compare(a.key, b.key)
		}
		return 0
	})
	return groups
}

// renderStatsGroups prints each group under a "<by> = <key>" header, the
// group's own summary and role table below it.
func renderStatsGroups(w io.Writer, groups []statsGroup, by statsBy) error {
	for i, g := range groups {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "%s = %s\n", by, g.key); err != nil {
			return err
		}
		if err := renderStats(w, g.records); err != nil {
			return err
		}
	}
	return nil
}

// statsGroupedRecord is the --json line of a grouped run: the Record plus the
// group it was counted under.
type statsGroupedRecord struct {
	Group string `json:"group"`
	dispatchrecord.Record
}
