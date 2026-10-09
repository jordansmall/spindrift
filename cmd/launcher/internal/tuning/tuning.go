// Package tuning renders the Tuning digest: a deterministic markdown summary
// of the Dispatch Records settled since a Chore's ledger cursor (the window),
// each figure set beside the same figure over the trailing seven days of
// older Records (the baseline), for the tuning Chore's Box to read (ADR 0062).
//
// Every row has a stable anchor derived from its identity alone, never from a
// value, so a finding can cite a row and a later digest can be matched to it:
//
//	summary:<metric>          e.g. summary:usd-per-record
//	role:<role>:<metric>      e.g. role:implement:avg-usd
//
// Each row also carries n, the window's sample size for that row; a row with
// n below the floor is marked thin. The output is not fenced: the prompt
// assembler fences it as untrusted input.
package tuning

import (
	"fmt"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/recordstats"
)

// BaselineWindow is how far back from now the baseline reaches.
const BaselineWindow = 7 * 24 * time.Hour

// absent renders a figure the baseline (or the delta against it) lacks.
const absent = "—"

// Digest is the rendered Tuning digest plus what the sweep needs from it.
type Digest struct {
	Text string
	// Latest is the ID of the newest window Record, the next Ledger cursor;
	// empty when the window is empty.
	Latest string
	// Records is the window size.
	Records int
}

// metric is one figure over a set of Records; ok is false when the set has no
// data for it (so no average exists).
type metric struct {
	v  float64
	ok bool
}

type row struct {
	anchor, label string
	n             int
	win, base     metric
	format        func(float64) string
	delta         func(float64) string
}

// Render builds the digest for the settled Records of records after cursor,
// against a baseline of the settled Records before them claimed within
// BaselineWindow of now. Rows with fewer than minSample window samples are
// marked thin.
func Render(records []dispatchrecord.Record, cursor string, now time.Time, minSample int) Digest {
	window := chore.NewSettledRecords(records, cursor)
	inWindow := make(map[string]bool, len(window))
	for _, r := range window {
		inWindow[r.ID] = true
	}
	from := now.Add(-BaselineWindow)
	var baseline []dispatchrecord.Record
	for _, r := range chore.NewSettledRecords(records, "") {
		if !inWindow[r.ID] && !r.ClaimTime.Before(from) && r.ClaimTime.Before(now) {
			baseline = append(baseline, r)
		}
	}

	d := Digest{Records: len(window)}
	if len(window) > 0 {
		d.Latest = window[len(window)-1].ID
	}
	start := cursor
	if start == "" {
		start = "(start)"
	}
	wRoles, wPasses, wUSD := recordstats.AggregateRoles(window)
	bRoles, bPasses, bUSD := recordstats.AggregateRoles(baseline)

	var b strings.Builder
	b.WriteString("# Tuning digest\n\n")
	fmt.Fprintf(&b, "Window: after %s through %s — %d settled Records, %d passes, notional cost $%.2f (API-equivalent).\n",
		start, orNone(d.Latest), len(window), wPasses, wUSD)
	fmt.Fprintf(&b, "Baseline: %d settled Records claimed %s to %s, %d passes.\n",
		len(baseline), from.UTC().Format("2006-01-02"), now.UTC().Format("2006-01-02"), bPasses)
	fmt.Fprintf(&b, "Rows with n < %d are marked thin. Δ is window minus baseline; %s means the baseline has no data.\n",
		minSample, absent)
	if len(window) == 0 {
		b.WriteString("\nNo new settled Records.\n")
		d.Text = b.String()
		return d
	}

	b.WriteString("\n## Summary\n\n")
	writeTable(&b, summaryRows(window, baseline, wPasses, bPasses, wUSD, bUSD), minSample)

	b.WriteString("\n## Roles\n\n")
	writeTable(&b, roleRows(wRoles, bRoles, len(window), len(baseline)), minSample)
	d.Text = b.String()
	return d
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func summaryRows(window, baseline []dispatchrecord.Record, wPasses, bPasses int, wUSD, bUSD float64) []row {
	perRecord := func(rs []dispatchrecord.Record, total float64) metric {
		if len(rs) == 0 {
			return metric{}
		}
		return metric{total / float64(len(rs)), true}
	}
	// Only work Dispatches land code, so the share is over work Records: a
	// window skewed toward research or butler Records must not read as a drop.
	workOnly := func(rs []dispatchrecord.Record) []dispatchrecord.Record {
		var out []dispatchrecord.Record
		for _, r := range rs {
			if r.Kind == dispatchkind.Work.Name {
				out = append(out, r)
			}
		}
		return out
	}
	landed := func(work []dispatchrecord.Record) metric {
		if len(work) == 0 {
			return metric{}
		}
		return metric{100 * float64(recordstats.LandedKeys(work)) / float64(len(work)), true}
	}
	wWork, bWork := workOnly(window), workOnly(baseline)
	n := len(window)
	return []row{
		{"summary:usd-per-record", "USD per Record", n, perRecord(window, wUSD), perRecord(baseline, bUSD), usd, signedUSD},
		{"summary:passes-per-record", "Passes per Record", n, perRecord(window, float64(wPasses)), perRecord(baseline, float64(bPasses)), num, signedNum},
		{"summary:landed-share", "Landed share of work Records", len(wWork), landed(wWork), landed(bWork), pct, signedPP},
	}
}

// roleRows builds the per-role table. Every Δ compares like with like: the
// window and the baseline differ in size, so passes are normalised per Record
// (winRecords, baseRecords) rather than compared as raw counts.
func roleRows(win, base []recordstats.RoleRow, winRecords, baseRecords int) []row {
	byRole := map[string]recordstats.RoleRow{}
	for _, r := range base {
		byRole[r.Role] = r
	}
	perPass := func(r recordstats.RoleRow, ok bool, f func(recordstats.RoleRow) float64) metric {
		if !ok || r.Passes == 0 {
			return metric{}
		}
		return metric{f(r) / float64(r.Passes), true}
	}
	// A baseline of Records holding none of this role's passes is a real 0.
	passesPerRecord := func(r recordstats.RoleRow, records int) metric {
		if records == 0 {
			return metric{}
		}
		return metric{float64(r.Passes) / float64(records), true}
	}
	usdOf := func(r recordstats.RoleRow) float64 { return r.USD }
	minOf := func(r recordstats.RoleRow) float64 {
		return float64(r.DurationMs) / float64(time.Minute.Milliseconds())
	}
	var out []row
	for _, w := range win { // already in pipeline order
		bl, has := byRole[w.Role]
		anchor := "role:" + w.Role + ":"
		out = append(out,
			row{anchor + "passes-per-record", w.Role + " passes per Record", winRecords, passesPerRecord(w, winRecords), passesPerRecord(bl, baseRecords), num, signedNum},
			row{anchor + "avg-usd", w.Role + " avg USD per pass", w.Passes, perPass(w, true, usdOf), perPass(bl, has, usdOf), usd, signedUSD},
			row{anchor + "avg-min", w.Role + " avg minutes per pass", w.Passes, perPass(w, true, minOf), perPass(bl, has, minOf), num, signedNum},
		)
		if wp, ok := w.BlockPercent(); ok {
			bp, bok := bl.BlockPercent()
			out = append(out, row{anchor + "block-rate", w.Role + " block rate", w.Verdicts, metric{wp, true}, metric{bp, bok && has}, pct, signedPP})
		}
	}
	return out
}

func usd(v float64) string       { return fmt.Sprintf("$%.2f", v) }
func num(v float64) string       { return fmt.Sprintf("%.1f", v) }
func pct(v float64) string       { return fmt.Sprintf("%.0f%%", v) }
func signedUSD(v float64) string { return fmt.Sprintf("%+.2f", v) }
func signedNum(v float64) string { return fmt.Sprintf("%+.1f", v) }
func signedPP(v float64) string  { return fmt.Sprintf("%+.0fpp", v) }

func writeTable(b *strings.Builder, rows []row, minSample int) {
	b.WriteString(tableHeader)
	for _, r := range rows {
		win, base, delta := absent, absent, absent
		if r.win.ok {
			win = r.format(r.win.v)
		}
		if r.base.ok {
			base = r.format(r.base.v)
			if r.win.ok {
				delta = r.delta(r.win.v - r.base.v)
			}
		}
		flag := ""
		if r.n < minSample {
			flag = "thin"
		}
		fmt.Fprintf(b, "| %s | %s | %d | %s | %s | %s | %s |\n",
			r.anchor, r.label, r.n, win, base, delta, flag)
	}
}
