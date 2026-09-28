package settle

import (
	"fmt"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// Finding is the promotion-relevant view of one well-formed butler finding,
// handed to a Runner's plan callback (issue #3990) before anything is filed
// so the callback can count eligible findings and reserve promotion room up
// front, the same order internal/butler's settle step already reserves
// Ledger slots before any PostIssue call.
type Finding struct {
	Class, Concurrence string
	DedupTerms         []string
}

// finding is in's promotion-relevant view, the projection both FileButlerFindings
// call sites (the plan callback and its per-finding decorate) need.
func (in issueIntent) finding() Finding {
	return Finding{Class: in.Class, Concurrence: in.Concurrence, DedupTerms: in.DedupTerms}
}

// Decoration is what a plan's per-finding callback adds to one finding's
// filing: an optional Backlink appended to its body, any ExtraLabels beyond
// the provenance label, and an OnFiled hook run only after PostIssue
// succeeds -- never on a failed or skipped intent (see
// fileIssueIntentsDetailedFunc's decorate contract) -- so a callback that
// spends shared state (like promotion room) can defer committing that spend
// until the filing it was for is real.
type Decoration struct {
	Backlink    string
	ExtraLabels []string
	OnFiled     func()
}

// FiledFinding is one successful filing -- never a failed or dedup-skipped
// one, since a caller acting on Filed (e.g. a Ledger done commit) must only
// ever record what actually landed: a failed filing's dedup key never
// reached the backlog, so recording it would suppress a later rotation's
// refile for good.
type FiledFinding struct {
	URL         string
	ExtraLabels []string
}

// LogRejectedSignals warns about result's rejected signal lines (see
// gate.go's logRejectedSignals) -- exported so a caller can log them before
// its own crash guards decide whether to file anything at all (issue #3990).
func LogRejectedSignals(num string, result dispatch.Result) {
	logRejectedSignals(num, result)
}

// FileButlerFindings caps result's issue-intent findings at maxPerSweep (0
// means no cap; see capIntents), hands the kept well-formed findings to plan
// before filing anything, then files the capped result under the
// "agent-butler-finding" provenance label using the Decoration the plan's
// returned callback produces per finding. It prints the same filed/dropped
// lines internal/butler's settle step always has, and returns only
// successful filings in payload order, plus the dropped count.
//
// The "agent-butler-finding" literal stays here, not a parameter:
// nix/checks/dispatch-labels.nix's comment names this file's occurrence of
// it by source text.
func FileButlerFindings(it forge.IssueTracker, num string, result dispatch.Result, maxPerSweep int, plan func(kept []Finding) func(Finding) Decoration) (filed []FiledFinding, dropped int) {
	kept, dropped := capIntents(result.IssueIntents, maxPerSweep)
	capped := result
	capped.IssueIntents = kept

	var findings []Finding
	for _, raw := range kept {
		in, ok := parseIssueIntent(raw)
		if !ok {
			continue
		}
		findings = append(findings, in.finding())
	}
	decide := plan(findings)

	rawFiled := fileIssueIntentsDetailedFunc(it, num, capped, "agent-butler-finding", func(in issueIntent) (string, []string, func()) {
		d := decide(in.finding())
		return d.Backlink, d.ExtraLabels, d.OnFiled
	})
	reportFiled(num, rawFiled)
	if dropped > 0 {
		fmt.Printf("    #%s  dropped %d finding(s) beyond the %d-per-sweep cap\n", num, dropped, maxPerSweep)
	}

	for _, f := range rawFiled {
		if f.Failed || f.Skipped {
			continue
		}
		filed = append(filed, FiledFinding{URL: f.URL, ExtraLabels: f.ExtraLabels})
	}
	return filed, dropped
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
		if _, ok := parseIssueIntent(r); !ok {
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
