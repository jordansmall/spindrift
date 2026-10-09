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
//	<dim>:<value>:<metric>    e.g. revision:abc123:usd-per-record
//	record:<id>               an Outliers row
//	evidence:<id>:<ordinal>:verdict|dispositions
//	                          a fenced Evidence item
//
// <dim> is revision, model, or prompt:<role> (so a prompt anchor carries a
// second colon-joined name before its value). A value is sanitised to letters,
// digits and . _ + - so it cannot break a table row or the anchor, and
// suffixed _2, _3, ... if two values collapse to one. A split dimension is
// shown only where the window and baseline hold more than one value of it.
//
// Quality rows (reverted, churn) join the summary, each role and each split
// value; their n counts only the Records that have matured.
//
// Outliers follow the tables: every failed, blocked, or ambiguous Record of
// the window, then the OutlierTopK dearest of the rest. Evidence quotes
// blocked review verdicts and fix dispositions, each in a promptfence block;
// an <id> is sanitised like a value.
//
// Each row also carries n, the window's sample size for that row; a row with
// n below the floor is marked thin. The output is not fenced: the prompt
// assembler fences it as untrusted input.
package tuning

import (
	"fmt"
	"slices"
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

// Limits are the knobs a digest is rendered under.
type Limits struct {
	// MinSample marks a row thin when fewer window Records back it.
	MinSample int
	// MaxBytes caps the digest's size; zero or less means uncapped. Evidence
	// is trimmed first and Outliers second, always from the tail; the
	// aggregate tables are never trimmed, so a cap below them is exceeded.
	MaxBytes int
}

// Render builds the digest for the settled Records of records after cursor,
// against a baseline of the settled Records before them claimed within
// BaselineWindow of now.
func Render(records []dispatchrecord.Record, cursor string, now time.Time, lim Limits) Digest {
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
	bRoles, bPasses, _ := recordstats.AggregateRoles(baseline)

	var b strings.Builder
	b.WriteString("# Tuning digest\n\n")
	fmt.Fprintf(&b, "Window: after %s through %s — %d settled Records, %d passes, notional cost $%.2f (API-equivalent).\n",
		start, recordstats.NoneIfEmpty(d.Latest), len(window), wPasses, wUSD)
	fmt.Fprintf(&b, "Baseline: %d settled Records claimed %s to %s, %d passes.\n",
		len(baseline), from.UTC().Format("2006-01-02"), now.UTC().Format("2006-01-02"), bPasses)
	fmt.Fprintf(&b, "Rows with n < %d are marked thin. Δ is window minus baseline; %s means the baseline has no data.\n",
		lim.MinSample, absent)
	if len(window) == 0 {
		b.WriteString("\nNo new settled Records.\n")
		d.Text = b.String()
		return d
	}

	b.WriteString("\n## Summary\n\n")
	writeTable(&b, groupRows("summary:", "", window, baseline), lim.MinSample)

	b.WriteString("\n## Roles\n\n")
	writeTable(&b, roleRows(wRoles, bRoles, window, baseline), lim.MinSample)

	if splits := splitSections(window, baseline); len(splits) > 0 {
		b.WriteString("\n## Splits\n")
		for _, s := range splits {
			fmt.Fprintf(&b, "\n### %s\n\n", s.name)
			writeTable(&b, s.rows, lim.MinSample)
		}
	}
	d.Text = trim(b.String(), outliers(window), evidence(window), lim.MaxBytes)
	return d
}

// trim appends the Outliers and Evidence sections to the aggregate text,
// dropping items from the tail (Evidence, then Outliers) until the result plus
// its trailer fits maxBytes. Sizes are sums of item lengths, so it never
// re-renders. The aggregates are kept whole, so a cap below them is exceeded.
// The trailer is written only when it fits beside what is kept.
func trim(aggregates string, out, ev section, maxBytes int) string {
	total := len(aggregates)
	for _, s := range []section{out, ev} {
		if len(s.items) > 0 {
			total += len(s.head)
		}
		for _, it := range s.items {
			total += len(it)
		}
	}
	nOut, nEv := len(out.items), len(ev.items)
	keptOut, keptEv := nOut, nEv
	trailer := func() string {
		return fmt.Sprintf("\n_Trimmed to fit BUTLER_TUNING_DIGEST_BYTES (%d bytes): %d evidence items and %d outliers omitted._\n",
			maxBytes, nEv-keptEv, nOut-keptOut)
	}
	drop := func(s section, kept *int) {
		*kept--
		total -= len(s.items[*kept])
		if *kept == 0 {
			total -= len(s.head)
		}
	}
	trimmed := false
	for maxBytes > 0 && (keptOut > 0 || keptEv > 0) {
		need := total
		if trimmed {
			need += len(trailer())
		}
		if need <= maxBytes {
			break
		}
		trimmed = true
		if keptEv > 0 {
			drop(ev, &keptEv)
		} else {
			drop(out, &keptOut)
		}
	}
	out.items, ev.items = out.items[:keptOut], ev.items[:keptEv]
	var b strings.Builder
	b.WriteString(aggregates)
	out.write(&b)
	ev.write(&b)
	if trimmed && total+len(trailer()) <= maxBytes {
		b.WriteString(trailer())
	}
	return b.String()
}

// groupRows builds the per-group figures for the Records of one group (the
// whole window, or one value of a split) beside the same group's baseline
// Records. prefix is the anchor stem (e.g. "revision:<value>:"), part of the
// stable cite contract; tag distinguishes the group in each Metric cell.
func groupRows(prefix, tag string, window, baseline []dispatchrecord.Record) []row {
	_, wPasses, wUSD := recordstats.AggregateRoles(window)
	_, bPasses, bUSD := recordstats.AggregateRoles(baseline)
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
	return append([]row{
		{prefix + "usd-per-record", "USD per Record" + tag, n, perRecord(window, wUSD), perRecord(baseline, bUSD), usd, signedUSD},
		{prefix + "passes-per-record", "Passes per Record" + tag, n, perRecord(window, float64(wPasses)), perRecord(baseline, float64(bPasses)), num, signedNum},
		{prefix + "landed-share", "Landed share of work Records" + tag, len(wWork), landed(wWork), landed(bWork), pct, signedPP},
	}, qualityRows(prefix, "Reverted share of merged work"+tag, "Mean 14-day churn"+tag, window, baseline)...)
}

// qualityRows are the reverted% and churn% rows of a group of Records. Both are
// filled in only once a Record matures, so each row's n counts the Records
// that carry the figure, not the group, and an immature group renders dashes.
func qualityRows(prefix, revertedLabel, churnLabel string, window, baseline []dispatchrecord.Record) []row {
	figure := func(f func([]dispatchrecord.Record) (float64, int), rs []dispatchrecord.Record) (metric, int) {
		v, filled := f(rs)
		return metric{v, filled > 0}, filled
	}
	wRev, wRevN := figure(recordstats.RevertedPercent, window)
	bRev, _ := figure(recordstats.RevertedPercent, baseline)
	wChurn, wChurnN := figure(recordstats.MeanChurnPercent, window)
	bChurn, _ := figure(recordstats.MeanChurnPercent, baseline)
	return []row{
		{prefix + "reverted", revertedLabel, wRevN, wRev, bRev, pct, signedPP},
		{prefix + "churn", churnLabel, wChurnN, wChurn, bChurn, pct, signedPP},
	}
}

// roleRows builds the per-role table. Every Δ compares like with like: the
// window and the baseline differ in size, so passes are normalised per Record
// rather than compared as raw counts. A role's reverted and churn figures are
// read over the Records that ran the role.
func roleRows(win, base []recordstats.RoleRow, window, baseline []dispatchrecord.Record) []row {
	winRecords, baseRecords := len(window), len(baseline)
	byRole := map[string]recordstats.RoleRow{}
	for _, r := range base {
		byRole[r.Role] = r
	}
	wHolding, bHolding := recordsByRole(window), recordsByRole(baseline)
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
	taken := map[string]bool{}
	for _, w := range win { // already in pipeline order
		bl, has := byRole[w.Role]
		role := unique(safe(w.Role), taken)
		anchor := "role:" + role + ":"
		out = append(out,
			row{anchor + "passes-per-record", role + " passes per Record", winRecords, passesPerRecord(w, winRecords), passesPerRecord(bl, baseRecords), num, signedNum},
			row{anchor + "avg-usd", role + " avg USD per pass", w.Passes, perPass(w, true, usdOf), perPass(bl, has, usdOf), usd, signedUSD},
			row{anchor + "avg-min", role + " avg minutes per pass", w.Passes, perPass(w, true, minOf), perPass(bl, has, minOf), num, signedNum},
		)
		if wp, ok := w.BlockPercent(); ok {
			bp, bok := bl.BlockPercent()
			out = append(out, row{anchor + "block-rate", role + " block rate", w.Verdicts, metric{wp, true}, metric{bp, bok && has}, pct, signedPP})
		}
		out = append(out, qualityRows(anchor, role+" reverted share", role+" mean 14-day churn", wHolding[w.Role], bHolding[w.Role])...)
	}
	return out
}

// recordsByRole maps each role to the Records holding one of its passes.
func recordsByRole(records []dispatchrecord.Record) map[string][]dispatchrecord.Record {
	out := map[string][]dispatchrecord.Record{}
	for _, g := range recordstats.GroupPasses(records, recordstats.RoleKey) {
		out[g.Key] = g.Records
	}
	return out
}

type splitSection struct {
	name string
	rows []row
}

// splitSections compares the window and baseline by revision, model, and each
// role's prompt hash. A dimension is shown only where the two between them
// hold more than one value of it; Records the dimension does not label are not
// a value, since "unknown" cannot be told apart from a change.
func splitSections(window, baseline []dispatchrecord.Record) []splitSection {
	byRecord := func(key func(dispatchrecord.Record) string) func([]dispatchrecord.Record) []recordstats.Group {
		return func(rs []dispatchrecord.Record) []recordstats.Group { return recordstats.GroupRecords(rs, key) }
	}
	byPass := func(key func(dispatchrecord.Pass) string) func([]dispatchrecord.Record) []recordstats.Group {
		return func(rs []dispatchrecord.Record) []recordstats.Group { return recordstats.GroupPasses(rs, key) }
	}
	type dimension struct {
		name  string
		group func([]dispatchrecord.Record) []recordstats.Group
	}
	dims := []dimension{
		{"revision", byRecord(recordstats.RevisionKey)},
		{"model", byPass(recordstats.ModelKey)},
	}
	for _, role := range recordstats.PromptRoles() {
		dims = append(dims, dimension{"prompt:" + role, byRecord(recordstats.PromptKey(role))})
	}

	var out []splitSection
	for _, dim := range dims {
		wg, bg := labelled(dim.group(window)), labelled(dim.group(baseline))
		var values []string
		for _, g := range wg {
			values = append(values, g.Key)
		}
		for _, g := range bg {
			if _, in := wg.find(g.Key); !in {
				values = append(values, g.Key)
			}
		}
		if len(values) < 2 {
			continue
		}
		// Suffixes are assigned in sorted order, not display order, so which of
		// two colliding values gets _2 does not change as the window moves.
		names := map[string]string{}
		taken := map[string]bool{}
		for _, v := range slices.Sorted(slices.Values(values)) {
			names[v] = unique(safe(v), taken)
		}
		var rows []row
		for _, v := range values {
			wr, _ := wg.find(v)
			br, _ := bg.find(v)
			sv := names[v]
			rows = append(rows, groupRows(dim.name+":"+sv+":", " ("+sv+")", wr, br)...)
		}
		out = append(out, splitSection{dim.name, rows})
	}
	return out
}

type labelledGroups []recordstats.Group

// labelled drops the (none) group: Records the dimension leaves unlabelled.
func labelled(groups []recordstats.Group) labelledGroups {
	var out labelledGroups
	for _, g := range groups {
		if g.Key != recordstats.None {
			out = append(out, g)
		}
	}
	return out
}

func (gs labelledGroups) find(key string) ([]dispatchrecord.Record, bool) {
	for _, g := range gs {
		if g.Key == key {
			return g.Records, true
		}
	}
	return nil, false
}

// safe maps a stamped value onto the anchor alphabet. Anchors and Metric
// cells are read back by a later digest and cited verbatim, and the values
// come from Records, so anything outside letters, digits and . _ + - (a pipe,
// a backtick, whitespace, the anchor separator) becomes "_".
func safe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '+', r == '-':
			return r
		}
		return '_'
	}, s)
}

// unique returns s, suffixed until it is not in taken, and records it. Two
// distinct values can collapse to one under safe; each must keep its own
// anchor.
func unique(s string, taken map[string]bool) string {
	out := s
	for i := 2; taken[out]; i++ {
		out = fmt.Sprintf("%s_%d", s, i)
	}
	taken[out] = true
	return out
}

func usd(v float64) string       { return fmt.Sprintf("$%.2f", v) }
func num(v float64) string       { return fmt.Sprintf("%.1f", v) }
func pct(v float64) string       { return fmt.Sprintf("%.0f%%", v) }
func signedUSD(v float64) string { return fmt.Sprintf("%+.2f", v) }
func signedNum(v float64) string { return fmt.Sprintf("%+.1f", v) }
func signedPP(v float64) string  { return fmt.Sprintf("%+.0fpp", v) }

func writeTable(b *strings.Builder, rows []row, minSample int) {
	b.WriteString("| Anchor | Metric | n | Window | Baseline | Δ | Flag |\n")
	b.WriteString("|---|---|---|---|---|---|---|\n")
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
