package settle

import (
	"fmt"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// Finding is the promotion-relevant view of one well-formed butler finding,
// handed to a Runner's plan callback (issue #3990) before anything is filed,
// so promotion room is reserved up front.
type Finding struct {
	Class, Concurrence string
	// Cites are the digest anchors a records finding argues from (issue
	// #4952); internal/butler checks them against the stored digest snapshot.
	Cites      []string
	DedupTerms []string
	// Patch is an optional unified diff (ADR 0057, issue #4072), carried
	// through unexamined; the host decides later, on Class, whether to apply it.
	Patch string
	// Title is the issue-intent's title, for a landed patch's commit subject
	// and PR title (issue #4074).
	Title string
	// Metric is the tuning finding's metric slug, the last part of its pattern key
	// (class, file, metric).
	Metric string
}

// finding is in's promotion-relevant view, the projection both FileButlerFindings
// call sites (the plan callback and its per-finding decorate) need.
func (in issueIntent) finding() Finding {
	return Finding{Class: in.Class, Concurrence: in.Concurrence, Cites: in.Cites, DedupTerms: in.DedupTerms, Patch: in.Patch, Title: in.Title, Metric: in.Metric}
}

// Decoration is what a plan's per-finding callback adds to one finding's
// filing: host-authored Backlink text appended to its body, ExtraLabels
// beyond the provenance label, and OnFiled, run with the filed URL only after
// PostIssue succeeds, never on a failed or skipped intent (see
// fileIssueIntentsDetailedFunc), so a callback spending shared state (like
// promotion room) commits the spend only once the filing is real.
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

// FileButlerFindings caps result's issue-intent findings at maxPerSweep (0 is
// no cap), hands the kept well-formed findings to plan before filing anything,
// then files them under findingLabel with the Decoration plan's callback
// returns per finding. Failed counts PostIssue failures only (a dedup skip or
// malformed payload is neither failed nor filed), so a caller can tell a
// sweep whose every filing failed from one that found nothing.
func FileButlerFindings(progress Progress, it forge.IssueTracker, num string, result dispatch.Result, maxPerSweep int, findingLabel string, dedup DedupPolicy, plan func(kept []Finding) func(Finding) Decoration) ButlerFiling {
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

	rawFiled := fileIssueIntentsDetailedFunc(progress, it, num, capped, findingLabel, dedup, func(in issueIntent) (string, []string, func(string)) {
		d := decide(in.finding())
		return d.Backlink, d.ExtraLabels, d.OnFiled
	})
	reportFiled(progress, num, rawFiled)
	if dropped > 0 {
		progress.Step(Step{Key: num, Detail: fmt.Sprintf("dropped %d finding(s) beyond the %d-per-sweep cap", dropped, maxPerSweep)})
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
// order; n<=0 returns raw itself, aliased. A malformed payload always passes
// through uncounted, left for the filer's own malformed-payload skip.
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
