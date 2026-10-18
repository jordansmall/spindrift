// Package recordstats aggregates Dispatch Records into the per-role and
// landed-key figures `spindrift stats` prints, so any other reader of the
// Records (the tuning Chore's digest) computes the same numbers.
package recordstats

import (
	"fmt"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/passmachine"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/usage"
)

// None groups a pass or Record with no value for the grouped field.
const None = "(none)"

// rolePipeline orders the known pass roles as a Dispatch runs them; any other
// role sorts after them alphabetically.
var rolePipeline = []passmachine.Role{
	passmachine.RoleImplement, passmachine.RoleReview, passmachine.RoleFix,
	passmachine.RoleLand, passmachine.RoleDeltaReview,
}

// RoleRank is a role's position in the Dispatch pipeline; unknown roles share
// the rank after the last known one.
func RoleRank(role string) int {
	if i := slices.Index(rolePipeline, passmachine.Role(role)); i >= 0 {
		return i
	}
	return len(rolePipeline)
}

// RoleRow is the totals of every pass one role ran.
type RoleRow struct {
	Role       string
	Passes     int
	USD        float64
	DurationMs int64
	APICalls   int
	Verdicts   int
	Blocks     int
}

// BlockPercent is the share of verdicts that blocked, as a percentage, and
// false when the role is not a review role or issued no verdict.
func (r RoleRow) BlockPercent() (float64, bool) {
	if passmachine.Role(r.Role).IsReview() && r.Verdicts > 0 {
		return 100 * float64(r.Blocks) / float64(r.Verdicts), true
	}
	return 0, false
}

// BlockRate is BlockPercent as a whole-percentage string, or "-" when there
// is none.
func (r RoleRow) BlockRate() string {
	if v, ok := r.BlockPercent(); ok {
		return fmt.Sprintf("%.0f%%", v)
	}
	return "-"
}

// AggregateRoles totals the passes of records by role, in pipeline order, and
// returns the pass count and notional USD across all of them.
func AggregateRoles(records []dispatchrecord.Record) (rows []RoleRow, passes int, usd float64) {
	byRole := map[string]*RoleRow{}
	for _, r := range records {
		for _, p := range r.Passes {
			role := NoneIfEmpty(p.Role)
			row := byRole[role]
			if row == nil {
				row = &RoleRow{Role: role}
				byRole[role] = row
			}
			row.Passes++
			row.USD += p.USD
			row.DurationMs += p.DurationMs
			row.APICalls += p.APICalls
			if p.Verdict != "" {
				row.Verdicts++
				if passmachine.Verdict(p.Verdict) == passmachine.VerdictBlock {
					row.Blocks++
				}
			}
			passes++
		}
		usd += RecordUSD(r)
	}
	for _, row := range byRole {
		rows = append(rows, *row)
	}
	slices.SortFunc(rows, func(a, b RoleRow) int {
		if ra, rb := RoleRank(a.Role), RoleRank(b.Role); ra != rb {
			return ra - rb
		}
		return strings.Compare(a.Role, b.Role)
	})
	return rows, passes, usd
}

// Merged reports whether the host settled r merged: its PR landed on the
// default branch.
func Merged(r dispatchrecord.Record) bool {
	return r.Outcome == forge.Complete.String() && r.Reason == settle.ReasonMerged
}

// LandedKeys counts distinct (kind, Dispatch key) pairs with a Record the host
// settled merged. Other complete reasons (a PR left open, already-resolved, a
// verdict, filed findings) did not put code on the default branch.
func LandedKeys(records []dispatchrecord.Record) int {
	type key struct{ kind, key string }
	seen := map[key]bool{}
	for _, r := range records {
		if Merged(r) {
			seen[key{r.Kind, r.DispatchKey}] = true
		}
	}
	return len(seen)
}

// RecordUSD is the notional cost of one Record: the sum of its passes' USD.
func RecordUSD(r dispatchrecord.Record) float64 {
	var total float64
	for _, p := range r.Passes {
		total += p.USD
	}
	return total
}

// AgentRow is the token and API-call totals of one agent across every pass it
// appeared in. There is no USD: Drivers price a pass, not an agent.
type AgentRow struct {
	Agent                    string
	Passes                   int
	APICalls                 int
	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
}

func (a AgentRow) totalTokens() int {
	return usage.AgentUsage{
		UncachedInputTokens:      a.InputTokens,
		OutputTokens:             a.OutputTokens,
		CacheReadInputTokens:     a.CacheReadInputTokens,
		CacheCreationInputTokens: a.CacheCreationInputTokens,
	}.TotalTokens()
}

// AggregateAgents totals the per-agent rows of every pass by agent: the main
// loop first, then the heaviest agent by total tokens, ties by name.
func AggregateAgents(records []dispatchrecord.Record) []AgentRow {
	byAgent := map[string]*AgentRow{}
	for _, r := range records {
		for _, p := range r.Passes {
			for _, a := range p.Agents {
				row := byAgent[a.Agent]
				if row == nil {
					row = &AgentRow{Agent: a.Agent}
					byAgent[a.Agent] = row
				}
				row.Passes++
				row.APICalls += a.APICalls
				row.InputTokens += a.InputTokens
				row.OutputTokens += a.OutputTokens
				row.CacheReadInputTokens += a.CacheReadInputTokens
				row.CacheCreationInputTokens += a.CacheCreationInputTokens
			}
		}
	}
	var rows []AgentRow
	for _, row := range byAgent {
		rows = append(rows, *row)
	}
	slices.SortFunc(rows, func(a, b AgentRow) int {
		if c := mainFirst(a.Agent, b.Agent); c != 0 {
			return c
		}
		if c := b.totalTokens() - a.totalTokens(); c != 0 {
			return c
		}
		return strings.Compare(a.Agent, b.Agent)
	})
	return rows
}
