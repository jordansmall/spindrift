package settle

import (
	"fmt"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
)

// Finding is the promotion-relevant view of one well-formed butler finding,
// handed to a Runner's plan callback (issue #3990) before anything is filed
// so the callback can count eligible findings and reserve promotion room up
// front, the same order internal/butler's settle step already reserves
// Ledger slots before any PostIssue call.
type Finding struct {
	Class, Concurrence, Metric string
	// Cites are the Tuning digest anchors a tuning finding argues from (issue
	// #4952); internal/butler checks them against the stored digest snapshot.
	Cites      []string
	DedupTerms []string
	// Patch is an optional unified diff (ADR 0057, issue #4072), carried
	// through unexamined: whether it is ever applied is the host's own
	// later decision, gated on Class, never this plan callback's.
	Patch string
	// Title is the issue-intent's own title, carried through for a landed
	// patch's commit subject/PR title (issue #4074) -- unexamined here, same
	// as Patch.
	Title string
}

// finding is in's promotion-relevant view, the projection both FileButlerFindings
// call sites (the plan callback and its per-finding decorate) need.
func (in issueIntent) finding() Finding {
	return Finding{Class: in.Class, Concurrence: in.Concurrence, Cites: in.Cites, Metric: in.Metric, DedupTerms: in.DedupTerms, Patch: in.Patch, Title: in.Title}
}

// Decoration is what a plan's per-finding callback adds to one finding's
// filing: an optional Backlink appended to its body, any ExtraLabels beyond
// the provenance label, and an OnFiled hook run only after PostIssue
// succeeds -- never on a failed or skipped intent (see
// fileIssueIntentsDetailedFunc's decorate contract), and handed the filed
// issue's own URL -- so a callback that spends shared state (like promotion
// room) can defer committing that spend, and record which URL it was spent
// on, until the filing it was for is real.
type Decoration struct {
	Backlink    string
	ExtraLabels []string
	OnFiled     func(url string)
}

// ButlerFiling is FileButlerFindings's tally: the URLs of the findings that
// filed in payload order, plus how many the per-sweep cap dropped and how many
// failed to post.
type ButlerFiling struct {
	Filed           []string
	Dropped, Failed int
}

// FileButlerFindings caps result's issue-intent findings at maxPerSweep (0
// means no cap; see capIntents), hands the kept well-formed findings to plan
// before filing anything, then files the capped result under
// dispatchkind.Butler.FindingLabel's provenance label using the Decoration
// the plan's returned callback produces per finding. It prints the same
// filed/dropped lines internal/butler's settle step always has, and returns
// only successful filings in payload order, plus the dropped count and the
// failed count (PostIssue failures only; a dedup skip or malformed payload is
// neither), so a caller can tell a sweep whose every filing failed from one
// that simply found nothing.
func FileButlerFindings(it forge.IssueTracker, num string, result dispatch.Result, maxPerSweep int, plan func(kept []Finding) func(Finding) Decoration) ButlerFiling {
	kept, dropped := capIntents(result.IssueIntents, maxPerSweep)
	capped := result
	capped.IssueIntents = kept

	var findings []Finding
	for _, raw := range kept {
		in, rej := parseIssueIntent(raw)
		if rej != nil {
			continue
		}
		findings = append(findings, in.finding())
	}
	decide := plan(findings)

	rawFiled := fileIssueIntentsDetailedFunc(it, num, capped, dispatchkind.Butler.FindingLabel, func(in issueIntent) (string, []string, func(string)) {
		d := decide(in.finding())
		return d.Backlink, d.ExtraLabels, d.OnFiled
	})
	reportFiled(num, rawFiled)
	if dropped > 0 {
		fmt.Printf("    #%s  dropped %d finding(s) beyond the %d-per-sweep cap\n", num, dropped, maxPerSweep)
	}

	out := ButlerFiling{Dropped: dropped}
	for _, f := range rawFiled {
		if f.Failed {
			out.Failed++
		}
		if f.Failed || f.Skipped {
			continue
		}
		out.Filed = append(out.Filed, f.URL)
	}
	return out
}

// capIntents keeps at most n of raw's well-formed issue-intent payloads, in
// order, dropping the rest; n<=0 means no cap, and raw is returned itself
// (the same backing array, aliased) rather than a copy. A malformed payload
// (parseIssueIntent rejects it) is neither capped nor counted as dropped --
// it always passes through kept, since fileIssueIntentsDetailedFunc's own
// malformed-payload skip, not this cap, is what decides its fate. n>0
// allocates and returns a fresh slice.
func capIntents(raw []string, n int) (kept []string, dropped int) {
	if n <= 0 {
		return raw, 0
	}
	kept = make([]string, 0, len(raw))
	wellFormed := 0
	for _, r := range raw {
		if _, rej := parseIssueIntent(r); rej != nil {
			kept = append(kept, r)
			continue
		}
		wellFormed++
		if wellFormed > n {
			dropped++
			continue
		}
		kept = append(kept, r)
	}
	return kept, dropped
}
