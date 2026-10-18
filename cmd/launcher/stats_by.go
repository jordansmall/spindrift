package main

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/recordstats"
)

type statsDim string

const (
	statsByRole     statsDim = "role"
	statsByAgent    statsDim = "agent"
	statsByRevision statsDim = "revision"
	statsByModel    statsDim = "model"
	statsByDriver   statsDim = "driver"
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

// statsKnobKey keys a Record by the value its dispatch_start stamp recorded for
// the knob: None when the knob is absent, statsEmpty when it was set to "".
func statsKnobKey(name string) func(dispatchrecord.Record) string {
	return func(r dispatchrecord.Record) string {
		v, ok := r.Knobs[name]
		switch {
		case !ok:
			return recordstats.None
		case v == "":
			return statsEmpty
		}
		return v
	}
}

// statsDimSpec is one row of the --by vocabulary. Parsing, its fallback error
// and grouping all read one row.
type statsDimSpec struct {
	dim statsDim
	// arg is set for a dimension spelled "dim:<arg>"; nil for a bare one.
	arg *statsDimArg
	// group splits Records by this dimension; arg is the validated text after "dim:".
	group func(records []dispatchrecord.Record, arg string) []recordstats.Group
}

// statsDimArg is the text a prefixed dimension takes after "dim:".
type statsDimArg struct {
	placeholder string // e.g. "<role>"
	parse       func(string) error
}

// spelling is the --by form the row accepts: "role", "prompt:<role>".
func (s statsDimSpec) spelling() string {
	if s.arg == nil {
		return string(s.dim)
	}
	return string(s.dim) + ":" + s.arg.placeholder
}

// statsDims is every dimension --by accepts, in the order the fallback error
// lists them.
var statsDims = []statsDimSpec{
	{dim: statsByRole, group: func(records []dispatchrecord.Record, _ string) []recordstats.Group {
		groups := recordstats.GroupPasses(records, recordstats.RoleKey)
		recordstats.SortRoleGroups(groups)
		return groups
	}},
	{dim: statsByAgent, group: func(records []dispatchrecord.Record, _ string) []recordstats.Group {
		return recordstats.GroupAgents(records)
	}},
	{dim: statsByRevision, group: func(records []dispatchrecord.Record, _ string) []recordstats.Group {
		return recordstats.GroupRecords(records, recordstats.RevisionKey)
	}},
	{dim: statsByModel, group: func(records []dispatchrecord.Record, _ string) []recordstats.Group {
		return recordstats.GroupPasses(records, recordstats.ModelKey)
	}},
	{dim: statsByDriver, group: func(records []dispatchrecord.Record, _ string) []recordstats.Group {
		return recordstats.GroupRecords(records, recordstats.DriverKey)
	}},
	{dim: statsByPrompt,
		arg: &statsDimArg{placeholder: "<role>", parse: func(role string) error {
			if roles := recordstats.PromptRoles(); !slices.Contains(roles, role) {
				return fmt.Errorf("unknown prompt role %q, want one of %s", role, strings.Join(roles, ", "))
			}
			return nil
		}},
		group: func(records []dispatchrecord.Record, role string) []recordstats.Group {
			return recordstats.GroupRecords(records, recordstats.PromptKey(role))
		}},
	{dim: statsByKnob,
		arg: &statsDimArg{placeholder: "<NAME>", parse: func(name string) error {
			known, secret := statsKnobNames()
			switch {
			case name == "":
				return errors.New("knob name is empty")
			case slices.Contains(secret, name):
				return fmt.Errorf("knob %s is secret, never recorded", name)
			case !slices.Contains(known, name):
				return fmt.Errorf("unknown knob %q", name)
			}
			return nil
		}},
		group: func(records []dispatchrecord.Record, name string) []recordstats.Group {
			return recordstats.GroupRecords(records, statsKnobKey(name))
		}},
}

// parseStatsBy checks one dimension against those stats can group by; its
// errors are the reason alone, for the caller to frame with the full --by value.
func parseStatsBy(by string) (statsBy, error) {
	for _, s := range statsDims {
		if s.arg == nil {
			if by == string(s.dim) {
				return statsBy{dim: s.dim}, nil
			}
			continue
		}
		// A bare "prompt" or "knob" has no prefix match and takes the generic error.
		if arg, ok := strings.CutPrefix(by, string(s.dim)+":"); ok {
			if err := s.arg.parse(arg); err != nil {
				return statsBy{}, err
			}
			return statsBy{dim: s.dim, arg: arg}, nil
		}
	}
	spellings := make([]string, len(statsDims))
	for i, s := range statsDims {
		spellings[i] = s.spelling()
	}
	last := len(spellings) - 1
	return statsBy{}, errors.New("want " + strings.Join(spellings[:last], ", ") + " or " + spellings[last])
}

// statsByList is a parsed --by value's dimensions in --by order; empty is no --by.
type statsByList []statsBy

func (l statsByList) has(dim statsDim) bool {
	return slices.ContainsFunc(l, func(b statsBy) bool { return b.dim == dim })
}

// plainTable reports whether the summary keeps the ungrouped per-role table:
// no --by, or --by role alone.
func (l statsByList) plainTable() bool {
	return len(l) == 0 || slices.Equal(l, statsByList{{dim: statsByRole}})
}

// parseStatsByList parses a comma-separated --by value into its ordered
// dimensions. An empty element or a repeated dimension is an error; two knobs
// or prompt roles are distinct dimensions.
func parseStatsByList(by string) (statsByList, error) {
	var dims statsByList
	for _, part := range strings.Split(by, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("invalid --by %q: empty dimension", by)
		}
		d, err := parseStatsBy(part)
		if err != nil {
			return nil, fmt.Errorf("invalid --by %q: element %q: %w", by, part, err)
		}
		if slices.Contains(dims, d) {
			return nil, fmt.Errorf("invalid --by %q: repeated dimension %q", by, d)
		}
		dims = append(dims, d)
	}
	return dims, nil
}

// statsGroup is one cell of a (possibly multi-dimension) --by grouping: one
// key per dimension, in --by order.
type statsGroup struct {
	keys    []string
	records []dispatchrecord.Record
}

// label is the group's keys joined in --by order, the JSON "group" value: a
// display label, ambiguous when a key contains ", " (group_keys is not).
func (g statsGroup) label() string { return strings.Join(g.keys, ", ") }

// groupStats nests the single-dimension groupings in --by order: the first
// dimension's groups, each split by the next, so a pass-level dimension after
// another sees only that group's passes.
func groupStats(records []dispatchrecord.Record, dims []statsBy) []statsGroup {
	groups := []statsGroup{{records: records}}
	for _, d := range dims {
		var next []statsGroup
		for _, g := range groups {
			for _, sub := range groupStatsBy(g.records, d) {
				next = append(next, statsGroup{keys: append(slices.Clone(g.keys), sub.Key), records: sub.Records})
			}
		}
		groups = next
	}
	return groups
}

// groupStatsBy splits claim-time-sorted Records into ordered groups by one
// dimension. Groups keep first-appearance order (the pipeline order for role),
// (none) last; a pass-level dimension (role, agent, model) is split per pass.
func groupStatsBy(records []dispatchrecord.Record, by statsBy) []recordstats.Group {
	for _, s := range statsDims {
		if s.dim == by.dim {
			return s.group(records, by.arg)
		}
	}
	panic("stats: cannot group by --by " + by.String())
}

// renderStatsGroups prints each group under a "<by> = <key>" header (one pair
// per dimension, joined by ", "), the group's own summary and role table below.
func renderStatsGroups(w io.Writer, groups []statsGroup, dims []statsBy) error {
	for i, g := range groups {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		pairs := make([]string, len(dims))
		for j, d := range dims {
			pairs[j] = d.String() + " = " + g.keys[j]
		}
		if _, err := fmt.Fprintln(w, strings.Join(pairs, ", ")); err != nil {
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
	Group     string   `json:"group"`
	GroupKeys []string `json:"group_keys"`
	dispatchrecord.Record
}
