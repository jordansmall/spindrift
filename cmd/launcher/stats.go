package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/passmachine"
	"spindrift.dev/launcher/internal/settle"
)

// statsRolePipeline orders the known pass roles as a Dispatch runs them; any
// other role sorts after them alphabetically.
var statsRolePipeline = []passmachine.Role{
	passmachine.RoleImplement, passmachine.RoleReview, passmachine.RoleFix,
	passmachine.RoleLand, passmachine.RoleDeltaReview,
}

const statsNone = "(none)"

// statsEmpty groups a stamped Record whose knob value is genuinely empty,
// apart from a Record with no snapshot of that knob (statsNone).
const statsEmpty = "(empty)"

type statsRoleRow struct {
	role       string
	passes     int
	usd        float64
	durationMs int64
	apiCalls   int
	verdicts   int
	blocks     int
}

type statsOptions struct {
	asJSON          bool
	reingest        bool
	roots           []string
	since           time.Time
	kind            string
	includeInferred bool
	by              statsBy
}

// parseStatsSince reads --since as RFC 3339 or a bare UTC date.
func parseStatsSince(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.DateOnly, v); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid --since %q: want RFC 3339 or YYYY-MM-DD", v)
}

// statsKinds lists the values --kind accepts: every dispatch kind, plus the
// unknown kind of a log the parser could not attribute.
func statsKinds() []string {
	kinds := make([]string, 0, len(dispatchkind.All)+1)
	for _, d := range dispatchkind.All {
		kinds = append(kinds, d.Name)
	}
	return append(kinds, dispatchrecord.KindUnknown)
}

func parseStatsArgs(args []string) (statsOptions, error) {
	opts := statsOptions{includeInferred: true}
	for i := 0; i < len(args); i++ {
		name, value, hasValue := strings.Cut(args[i], "=")
		switch name {
		case "--json", "--reingest":
			if hasValue {
				return opts, fmt.Errorf("unrecognized argument: %s", args[i])
			}
			if name == "--json" {
				opts.asJSON = true
			} else {
				opts.reingest = true
			}
		case "--include-inferred":
			opts.includeInferred = true
			if hasValue {
				b, err := strconv.ParseBool(value)
				if err != nil {
					return opts, fmt.Errorf("invalid --include-inferred %q: want true or false", value)
				}
				opts.includeInferred = b
			}
		case "--root", "--since", "--kind", "--by":
			if !hasValue {
				if i+1 >= len(args) {
					return opts, fmt.Errorf("flag %s requires a value", name)
				}
				i++
				value = args[i]
			}
			if value == "" {
				return opts, fmt.Errorf("flag %s requires a non-empty value", name)
			}
			switch name {
			case "--root":
				opts.roots = append(opts.roots, value)
			case "--kind":
				if !slices.Contains(statsKinds(), value) {
					return opts, fmt.Errorf("invalid --kind %q: want one of %s", value, strings.Join(statsKinds(), ", "))
				}
				opts.kind = value
			case "--by":
				by, err := parseStatsBy(value)
				if err != nil {
					return opts, err
				}
				opts.by = by
			case "--since":
				t, err := parseStatsSince(value)
				if err != nil {
					return opts, err
				}
				opts.since = t
			}
		default:
			return opts, fmt.Errorf("unrecognized argument: %s", args[i])
		}
	}
	return opts, nil
}

// keep reports whether a Record passes the --since, --kind, and
// --include-inferred filters.
func (o statsOptions) keep(r dispatchrecord.Record) bool {
	if !o.since.IsZero() && r.ClaimTime.Before(o.since) {
		return false
	}
	if o.kind != "" && r.Kind != o.kind {
		return false
	}
	return o.includeInferred || r.Attribution != dispatchrecord.AttributionInferred
}

// statsRootRecords ingests one root's logs and returns its Records.
func statsRootRecords(root string, reingest, fillRevisions bool, stderr io.Writer) ([]dispatchrecord.Record, error) {
	store, err := dispatchrecord.Open(root)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	ingest := store.Ingest
	if reingest {
		ingest = store.Reingest
	}
	if _, err := ingest(); err != nil {
		return nil, err
	}
	records, err := store.Records()
	if err != nil {
		return nil, err
	}
	// The Events fallback only feeds the revision grouping: elsewhere it would
	// stamp a field on inferred Records that every other view omits, and cost
	// two git subprocesses per root.
	if fillRevisions {
		fillInferredRevisions(records, checkoutEvents(root, stderr))
	}
	return records, nil
}

func cmdStats(args []string, stdout, stderr io.Writer) int {
	opts, err := parseStatsArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		fmt.Fprintln(stderr, subcommandUsage("stats"))
		return 1
	}
	roots := opts.roots
	if len(roots) == 0 {
		roots = []string{"."}
	}
	var records []dispatchrecord.Record
	seen := map[string]bool{}
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", err)
			return 1
		}
		// Opening a store creates its directory, so a mistyped root must fail
		// here rather than leave an empty .spindrift behind.
		info, err := os.Stat(abs)
		if err != nil {
			fmt.Fprintf(stderr, "--root %s: %v\n", root, err)
			return 1
		}
		if !info.IsDir() {
			fmt.Fprintf(stderr, "--root %s: not a directory\n", root)
			return 1
		}
		// A root named twice, or through a symlink, would otherwise double
		// every Record. Only the dedup key is resolved: Records keep the path
		// as named.
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			fmt.Fprintf(stderr, "--root %s: %v\n", root, err)
			return 1
		}
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		recs, err := statsRootRecords(abs, opts.reingest, opts.by.dim == statsByRevision, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", err)
			return 1
		}
		for _, r := range recs {
			if opts.keep(r) {
				records = append(records, r)
			}
		}
	}
	slices.SortStableFunc(records, func(a, b dispatchrecord.Record) int {
		return cmp.Or(a.ClaimTime.Compare(b.ClaimTime), strings.Compare(a.Root, b.Root), strings.Compare(a.ID, b.ID))
	})
	var groups []statsGroup
	if opts.by.dim != "" {
		groups = groupStats(records, opts.by)
	}
	if opts.asJSON {
		if err := encodeStatsJSON(json.NewEncoder(stdout), records, groups, opts.by); err != nil {
			fmt.Fprintf(stderr, "%s\n", err)
			return 1
		}
		return 0
	}
	if opts.by.dim == "" || opts.by.dim == statsByRole {
		err = renderStats(stdout, records)
	} else {
		err = renderStatsGroups(stdout, groups, opts.by)
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}
	return 0
}

// encodeStatsJSON writes one line per Record, tagged with its group when --by
// is set.
func encodeStatsJSON(enc *json.Encoder, records []dispatchrecord.Record, groups []statsGroup, by statsBy) error {
	if by.dim == "" {
		for _, r := range records {
			if err := enc.Encode(r); err != nil {
				return err
			}
		}
		return nil
	}
	for _, g := range groups {
		for _, r := range g.records {
			if err := enc.Encode(statsGroupedRecord{Group: g.key, Record: r}); err != nil {
				return err
			}
		}
	}
	return nil
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
				role = statsNone
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

// landedKeys counts distinct (kind, Dispatch key) pairs with a Record the host
// settled merged. Other complete reasons (a PR left open, already-resolved, a
// verdict, filed findings) did not put code on the default branch.
func landedKeys(records []dispatchrecord.Record) int {
	type key struct{ kind, key string }
	seen := map[key]bool{}
	for _, r := range records {
		if r.Outcome == forge.Complete.String() && r.Reason == settle.ReasonMerged {
			seen[key{r.Kind, r.DispatchKey}] = true
		}
	}
	return len(seen)
}

// outcomeSources counts Records whose outcome is the host's dispatch_settled
// op and the rest, which have none.
func outcomeSources(records []dispatchrecord.Record) (settled, none int) {
	for _, r := range records {
		if r.OutcomeSource == dispatchrecord.OutcomeSourceSettled {
			settled++
		} else {
			none++
		}
	}
	return settled, none
}

func renderStats(w io.Writer, records []dispatchrecord.Record) error {
	rows, passes, usd := aggregateStatsRoles(records)
	landed := landedKeys(records)
	perLanded := "-"
	if landed > 0 {
		perLanded = fmt.Sprintf("$%.2f", usd/float64(landed))
	}
	settled, none := outcomeSources(records)
	if _, err := fmt.Fprintf(w, "Records: %d  Passes: %d  Notional USD: $%.2f (API-equivalent)  Landed keys: %d  USD per landed key: %s  Outcome source: %s %d, %s %d\n",
		len(records), passes, usd, landed, perLanded,
		dispatchrecord.OutcomeSourceSettled, settled, dispatchrecord.OutcomeSourceNone, none); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ROLE\tPASSES\tUSD\tAVG_USD\tAVG_MIN\tAPI_CALLS\tBLOCK_RATE")
	for _, row := range rows {
		blockRate := "-"
		if passmachine.Role(row.role).IsReview() && row.verdicts > 0 {
			blockRate = fmt.Sprintf("%.0f%%", 100*float64(row.blocks)/float64(row.verdicts))
		}
		n := float64(row.passes)
		fmt.Fprintf(tw, "%s\t%d\t$%.2f\t$%.2f\t%.1f\t%d\t%s\n",
			row.role, row.passes, row.usd, row.usd/n, (time.Duration(row.durationMs)*time.Millisecond).Minutes()/n, row.apiCalls, blockRate)
	}
	return tw.Flush()
}
