package main

import (
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
		if roles := recordstats.PromptRoles(); !slices.Contains(roles, role) {
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

// groupStats splits claim-time-sorted Records into ordered groups by a --by
// dimension. Groups keep first-appearance order (the pipeline order for role),
// (none) last; a pass-level dimension (role, model) is split per pass.
func groupStats(records []dispatchrecord.Record, by statsBy) []recordstats.Group {
	switch by.dim {
	case statsByRole:
		groups := recordstats.GroupPasses(records, recordstats.RoleKey)
		recordstats.SortRoleGroups(groups)
		return groups
	case statsByModel:
		return recordstats.GroupPasses(records, recordstats.ModelKey)
	case statsByRevision:
		return recordstats.GroupRecords(records, recordstats.RevisionKey)
	case statsByPrompt:
		return recordstats.GroupRecords(records, recordstats.PromptKey(by.arg))
	case statsByKnob:
		return recordstats.GroupRecords(records, func(r dispatchrecord.Record) string {
			v, ok := r.Knobs[by.arg]
			switch {
			case !ok:
				return recordstats.None
			case v == "":
				return statsEmpty
			}
			return v
		})
	}
	panic("stats: cannot group by --by " + by.String())
}

// renderStatsGroups prints each group under a "<by> = <key>" header, the
// group's own summary and role table below it.
func renderStatsGroups(w io.Writer, groups []recordstats.Group, by statsBy) error {
	for i, g := range groups {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "%s = %s\n", by, g.Key); err != nil {
			return err
		}
		if err := renderStats(w, g.Records); err != nil {
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
