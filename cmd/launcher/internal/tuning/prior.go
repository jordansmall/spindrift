package tuning

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/recordstats"
)

// PriorFindingsN is how many prior tuning findings the Prior findings section
// lists, newest first.
const PriorFindingsN = 10

// Prior is a tuning finding the backlog already holds, as the Prior findings
// section needs it.
type Prior struct {
	Number string
	// Open is the issue's state; a closed one is closed-not-planned when
	// NotPlanned, else closed-completed.
	Open, NotPlanned bool
	// Metric is the finding's metric slug.
	Metric string
	// Body is the issue body, read only for the anchors its Evidence table cites.
	Body string
}

// MergeLookup reports whether the PR at prURL merged, and the commit it landed
// as ("" when the forge cannot say). It is a forge round trip, so priorFindings
// calls it at most once per listed finding.
type MergeLookup func(prURL string) (commit string, ok bool)

// priorFindings renders the newest PriorFindingsN findings of prior. A finding
// whose closing work Dispatch merged, per its Record or per merged for a PR a
// human landed after the Record settled, also gets that PR's merge commit
// and its metric's figure over the Records settled after the merge beside
// those settled before it.
func priorFindings(prior []Prior, records []dispatchrecord.Record, minSample int, merged MergeLookup) section {
	byNumber := map[string]Prior{}
	for _, p := range prior {
		byNumber[p.Number] = p
	}
	list := slices.SortedFunc(maps.Values(byNumber), func(a, b Prior) int { return -compareNumbers(a.Number, b.Number) })
	list = list[:min(len(list), PriorFindingsN)]

	s := section{noun: "prior findings", head: fmt.Sprintf("\n"+priorHeading+"\n\nThe last %d tuning findings, newest first. For a merged one, Δ is the metric after "+
		"its closing PR's merge minus before it, over the Records settled on each side; other states carry no Δ.\n\n"+
		"| Anchor | State | Revision | Metric | n | Before | After | Δ | Flag |\n|---|---|---|---|---|---|---|---|---|\n", len(list))}
	settled := chore.NewSettledRecords(records, "")
	for _, p := range list {
		state, revision, n, before, after, delta, flag := "open", absent, absent, absent, absent, absent, ""
		merge, commit, isMerged := mergingRecord(settled, p.Number, merged)
		switch {
		case isMerged:
			state, revision = "merged", cell(commit)
			if r, ok := mergeRow(settled, merge, p); ok {
				after, before, delta = r.cells()
				n = strconv.Itoa(r.n)
				if r.n < minSample {
					flag = "thin"
				}
			}
		case p.Open:
		case p.NotPlanned:
			state = "closed-not-planned"
		default:
			state = "closed-completed"
		}
		s.items = append(s.items, fmt.Sprintf(priorRowPrefix+"%s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			safe(p.Number), state, revision, cell(p.Metric), n, before, after, delta, flag))
	}
	return s
}

// compareNumbers orders issue keys by their non-numeric prefix, then their
// trailing integer, so Jira's PROJ-12 is newer than PROJ-9; plain numbers have
// an empty prefix. A key with no trailing integer compares as a string.
func compareNumbers(a, b string) int {
	pa, na, okA := splitKey(a)
	pb, nb, okB := splitKey(b)
	if !okA || !okB {
		return cmp.Compare(a, b)
	}
	return cmp.Or(cmp.Compare(pa, pb), cmp.Compare(na, nb))
}

// splitKey splits key into its prefix and trailing integer.
func splitKey(key string) (prefix string, n int, ok bool) {
	i := len(strings.TrimRight(key, "0123456789"))
	n, err := strconv.Atoi(key[i:])
	return key[:i], n, err == nil
}

// mergingRecord is the latest settled work Record that merged issue's PR, with
// the commit it landed as. A work PR closing a tuning finding is held for a
// human, so its Record settles unmerged; merged asks the forge about the latest
// PR-bearing Record, which covers a merge the late-merge sweep has not yet
// upgraded or that fell outside its window.
func mergingRecord(settled []dispatchrecord.Record, issue string, merged MergeLookup) (rec dispatchrecord.Record, commit string, ok bool) {
	var held *dispatchrecord.Record
	for _, r := range slices.Backward(settled) {
		if d, ok := dispatchkind.ByName(r.Kind); !ok || d.Contract != dispatchkind.ContractLanding || r.DispatchKey != issue {
			continue
		}
		if recordstats.Merged(r) {
			return r, r.MergeCommit, true
		}
		if held == nil && r.PRURL != "" {
			held = &r
		}
	}
	if held != nil && merged != nil {
		if commit, ok := merged(held.PRURL); ok {
			return *held, commit, true
		}
	}
	return dispatchrecord.Record{}, "", false
}

// mergeRow is the aggregate row the finding cites, recomputed over the Records
// settled after merge (the window) against those settled before it (the
// baseline). The row comes from the host's own aggregates, so a hand-edited
// body can name which one but cannot supply text.
//
// No settle time is stored, so a Dispatch claimed before the merge but settled
// after it counts as after. For a PR a human merged after its Record settled
// held, the cutoff is that settle: Records settled between the hold and the
// merge count as after.
func mergeRow(settled []dispatchrecord.Record, merge dispatchrecord.Record, p Prior) (row, bool) {
	if p.Metric == "" {
		return row{}, false
	}
	var after, before []dispatchrecord.Record
	from := merge.ClaimTime.Add(-BaselineWindow)
	for _, r := range settled {
		switch {
		case r.SettledSeq > merge.SettledSeq:
			after = append(after, r)
		case r.SettledSeq < merge.SettledSeq && !r.ClaimTime.Before(from) && r.ClaimTime.Before(merge.ClaimTime):
			before = append(before, r)
		}
	}
	if len(after) == 0 {
		return row{}, false
	}
	anchor := "summary:" + p.Metric
	for _, a := range citedAnchors(p.Body) {
		if strings.HasSuffix(a, ":"+p.Metric) {
			anchor = a
			break
		}
	}
	for _, r := range aggregateRows(after, before) {
		if r.anchor == anchor {
			return r, true
		}
	}
	return row{}, false
}

// citedAnchors returns the anchors of the Evidence table in a finding's issue
// body, in cite order.
func citedAnchors(body string) []string {
	var out []string
	in := false
	for _, line := range strings.Split(body, "\n") {
		switch {
		case line == evidenceHeading:
			in = true
		case strings.HasPrefix(line, "## "):
			in = false
		case in && strings.HasPrefix(line, "| "):
			if a, _, ok := strings.Cut(strings.TrimPrefix(line, "| "), " |"); ok {
				out = append(out, a)
			}
		}
	}
	return out
}

// aggregateRows is every row the Summary, Roles and Splits tables would hold
// for window against baseline.
func aggregateRows(window, baseline []dispatchrecord.Record) []row {
	wRoles, _, _ := recordstats.AggregateRoles(window)
	bRoles, _, _ := recordstats.AggregateRoles(baseline)
	rows := append(groupRows("summary:", "", window, baseline), roleRows(wRoles, bRoles, window, baseline)...)
	for _, s := range splitSections(window, baseline) {
		rows = append(rows, s.rows...)
	}
	return rows
}
