package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/passmachine"
)

// statsRolePipeline orders the known pass roles as a Dispatch runs them; any
// other role sorts after them alphabetically.
var statsRolePipeline = []passmachine.Role{
	passmachine.RoleImplement, passmachine.RoleReview, passmachine.RoleFix,
	passmachine.RoleLand, passmachine.RoleDeltaReview,
}

// statsBlockRoles are the roles whose passes end in an APPROVE/BLOCK verdict.
var statsBlockRoles = []passmachine.Role{passmachine.RoleReview, passmachine.RoleDeltaReview}

const statsNoRole = "(none)"

type statsRoleRow struct {
	role       string
	passes     int
	usd        float64
	durationMs int64
	apiCalls   int
	verdicts   int
	blocks     int
}

func cmdStats(args []string, stdout, stderr io.Writer) int {
	asJSON := false
	for _, a := range args {
		if a != "--json" {
			fmt.Fprintf(stderr, "unrecognized argument: %s\n", a)
			fmt.Fprintln(stderr, "usage: spindrift stats [--json]")
			return 1
		}
		asJSON = true
	}
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}
	store, err := dispatchrecord.Open(root)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}
	defer store.Close()
	if _, err := store.Ingest(); err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}
	records, err := store.Records()
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		for _, r := range records {
			if err := enc.Encode(r); err != nil {
				fmt.Fprintf(stderr, "%s\n", err)
				return 1
			}
		}
		return 0
	}
	if err := renderStats(stdout, records); err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}
	return 0
}

func statsRoleRank(role string) int {
	if i := slices.Index(statsRolePipeline, passmachine.Role(role)); i >= 0 {
		return i
	}
	return len(statsRolePipeline)
}

func aggregateStatsRoles(records []dispatchrecord.Record) (rows []statsRoleRow, passes int, usd float64) {
	byRole := map[string]*statsRoleRow{}
	for _, r := range records {
		for _, p := range r.Passes {
			role := p.Role
			if role == "" {
				role = statsNoRole
			}
			row := byRole[role]
			if row == nil {
				row = &statsRoleRow{role: role}
				byRole[role] = row
			}
			row.passes++
			row.usd += p.USD
			row.durationMs += p.DurationMs
			row.apiCalls += p.APICalls
			if p.Verdict != "" {
				row.verdicts++
				if passmachine.Verdict(p.Verdict) == passmachine.VerdictBlock {
					row.blocks++
				}
			}
			passes++
			usd += p.USD
		}
	}
	for _, row := range byRole {
		rows = append(rows, *row)
	}
	slices.SortFunc(rows, func(a, b statsRoleRow) int {
		if ra, rb := statsRoleRank(a.role), statsRoleRank(b.role); ra != rb {
			return ra - rb
		}
		return strings.Compare(a.role, b.role)
	})
	return rows, passes, usd
}

func renderStats(w io.Writer, records []dispatchrecord.Record) error {
	rows, passes, usd := aggregateStatsRoles(records)
	fmt.Fprintf(w, "Records: %d  Passes: %d  Notional USD: $%.2f (API-equivalent)\n", len(records), passes, usd)
	if len(rows) == 0 {
		return nil
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ROLE\tPASSES\tUSD\tAVG_USD\tAVG_MIN\tAPI_CALLS\tBLOCK_RATE")
	for _, row := range rows {
		blockRate := "-"
		if slices.Contains(statsBlockRoles, passmachine.Role(row.role)) && row.verdicts > 0 {
			blockRate = fmt.Sprintf("%.0f%%", 100*float64(row.blocks)/float64(row.verdicts))
		}
		n := float64(row.passes)
		fmt.Fprintf(tw, "%s\t%d\t$%.2f\t$%.2f\t%.1f\t%d\t%s\n",
			row.role, row.passes, row.usd, row.usd/n, (time.Duration(row.durationMs)*time.Millisecond).Minutes()/n, row.apiCalls, blockRate)
	}
	return tw.Flush()
}
