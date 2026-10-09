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
			role := p.Role
			if role == "" {
				role = None
			}
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
			usd += p.USD
		}
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

// LandedKeys counts distinct (kind, Dispatch key) pairs with a Record the host
// settled merged. Other complete reasons (a PR left open, already-resolved, a
// verdict, filed findings) did not put code on the default branch.
func LandedKeys(records []dispatchrecord.Record) int {
	type key struct{ kind, key string }
	seen := map[key]bool{}
	for _, r := range records {
		if r.Outcome == forge.Complete.String() && r.Reason == settle.ReasonMerged {
			seen[key{r.Kind, r.DispatchKey}] = true
		}
	}
	return len(seen)
}
