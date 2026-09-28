package butler

import (
	"fmt"
	"slices"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/settle"
)

// settled is what one settleRun.settle call reports back to Sweep: whether
// the done commit actually landed, and, only when it did, the counts Sweep's
// Outcome carries. Sweep learns "did it land" from this return value alone --
// it never re-reads the Ledger after settle to find out, since a lost race
// on the done commit and a crashed Box (no ready outcome at all) both leave
// nothing new to read back.
type settled struct {
	done                     bool
	filed, promoted, dropped int
}

// settleRun is one Chore run's settle step: file each finding the Box reported, then write the Chore's
// done Ledger commit carrying the advanced lastSwept/cursor, the filed URLs,
// and the run's usage. No CI watch, no merge, and no tracker label
// transition -- the butler carries no tracker issue of its own, only a
// Ledger Chore.
type settleRun struct {
	it                  forge.IssueTracker
	ledger              ledger.Backend
	chore               string
	claim               ledger.Tip
	scope               chore.Scope
	now                 func() time.Time
	maxFindingsPerSweep int
	policy              promotion
}

// newSettleRun constructs a settleRun for one Chore run. claim is the Ledger
// tip Claim produced at the start of this run -- ledger.Finish's compare-and-
// swap parent, unless settle first reserves promotion slots (issue #3926), in
// which case the reservation commit takes over as parent; scope is the run's
// computed Scope (internal/chore.NextScope), whose Head/NextCursor become the
// done commit's lastSwept/cursor on success. maxFindingsPerSweep caps how
// many well-formed findings settle will file in one sweep; 0 means no cap.
// policy is the host-side auto-promotion gate (issue #3880); its zero value
// never promotes anything.
func newSettleRun(it forge.IssueTracker, backend ledger.Backend, choreName string, claim ledger.Tip, scope chore.Scope, now func() time.Time, maxFindingsPerSweep int, policy promotion) *settleRun {
	return &settleRun{it: it, ledger: backend, chore: choreName, claim: claim, scope: scope, now: now, maxFindingsPerSweep: maxFindingsPerSweep, policy: policy}
}

// settle files result's findings, if any, then writes the Chore's done
// Ledger commit. A crashed run -- no outcome line, or an outcome line whose
// status isn't "ready" (e.g. blocked) -- files nothing and writes no Ledger
// commit at all, so the claim stands and lastSwept/cursor stay put for the
// next run to resume from (ADR 0056). Rejected signal lines are warned about
// first, before either crash guard, so a crashed run's dropped comment/
// issue-intent/pr-intent lines are never silently lost (issue #3990).
func (s *settleRun) settle(d dispatch.Dispatcher, result dispatch.Result) settled {
	num := dispatchkey.Chore(s.chore).String()
	settle.LogRejectedSignals(num, result)

	if !result.Resolved.Found {
		s.fail(num, "no ready outcome line")
		return settled{}
	}
	o := result.Resolved.Outcome
	if o.Status != outcome.StatusReady {
		note := o.Note
		if note == "" {
			note = "status=" + o.Status
		}
		s.fail(num, note)
		return settled{}
	}

	finishParent := s.claim
	filed, dropped := settle.FileButlerFindings(s.it, num, result, s.maxFindingsPerSweep, func(kept []settle.Finding) func(settle.Finding) settle.Decoration {
		// Room is evaluated at most once per settle, and only when this Chore
		// has an allow-list at all -- a Chore with no Classes can never
		// promote, so spending a Ledger walk on Room for it would be waste.
		// Evaluating at settle time rather than at run start means a
		// promotion whose reservation commit already landed is counted here;
		// it is still a soft cap like ADR 0056's other budgets, not a hard
		// one -- two runs settling at the same moment can each read the same
		// total and both spend it.
		remaining := 0
		if len(s.policy.Classes) > 0 && s.policy.Room != nil {
			remaining = s.policy.Room()
		}

		// Reserve the slots this run intends to spend before filing anything
		// (issue #3926): if the done commit below never lands -- the claim
		// was lost to a takeover, or the push itself fails -- the
		// reservation still counts against DayTotals, so a lost Finish can
		// never let a Chore promote past the day's budget. finishParent
		// moves to the reservation tip on success so the done commit's own
		// CAS is checked against it, not the stale claim.
		if remaining > 0 {
			eligible := 0
			for _, f := range kept {
				if s.policy.eligible(f, butlerFiles(f.DedupTerms)) {
					eligible++
				}
			}
			n := min(remaining, eligible)
			if n > 0 {
				reserved, err := ledger.Reserve(s.ledger, s.chore, s.claim, n, s.now())
				if err != nil {
					fmt.Printf("    #%s  status=promotion-reserve-failed  !! %v\n", num, err)
					remaining = 0
				} else {
					remaining = n
					finishParent = reserved
				}
			}
		}

		return func(f settle.Finding) settle.Decoration {
			backlink := butlerBacklink(s.chore, f)
			files := butlerFiles(f.DedupTerms)
			if !s.policy.eligible(f, files) || remaining <= 0 {
				return settle.Decoration{Backlink: backlink}
			}
			note := promotionNote(s.chore, f, s.policy, len(files))
			// Spend the slot only in OnFiled, after PostIssue succeeds, so a
			// failed post frees it back to the rest of the sweep.
			return settle.Decoration{Backlink: backlink + "\n\n" + note, ExtraLabels: []string{s.policy.Label}, OnFiled: func() { remaining-- }}
		}
	})

	var urls, promoted []string
	for _, f := range filed {
		urls = append(urls, f.URL)
		if s.policy.Label != "" && slices.Contains(f.ExtraLabels, s.policy.Label) {
			promoted = append(promoted, f.URL)
		}
	}

	state := ledger.State{
		LastSwept: s.scope.Head,
		Cursor:    s.scope.NextCursor,
		Filed:     urls,
		Promoted:  promoted,
		Usage:     d.CumulativeUsage(),
		Dropped:   dropped,
	}
	if _, err := ledger.Finish(s.ledger, s.chore, finishParent, state, s.now()); err != nil {
		fmt.Printf("    #%s  status=ledger-finish-failed  !! %v\n", num, err)
		report.Settled(dispatchkey.Chore(s.chore), forge.Failed.String(), fmt.Sprintf("ledger finish failed: %v", err))
		return settled{}
	}

	note := fmt.Sprintf("%d filed", len(urls))
	if len(promoted) > 0 {
		note = fmt.Sprintf("%d filed, %d promoted", len(urls), len(promoted))
	}
	if dropped > 0 {
		note = fmt.Sprintf("%s, %d dropped", note, dropped)
	}
	report.Settled(dispatchkey.Chore(s.chore), forge.Complete.String(), note)
	fmt.Printf("    #%s  status=%s  note=%s\n", num, o.Status, note)

	return settled{done: true, filed: len(urls), promoted: len(promoted), dropped: dropped}
}

// fail prints a status=failed line and reports the run failed. It applies no
// tracker transition: the butler has no tracker issue to move. Neither files
// findings nor writes a Ledger commit, so the claim stands and
// lastSwept/cursor stay at the prior run's values for the next run to resume
// from.
func (s *settleRun) fail(num, note string) {
	report.Settled(dispatchkey.Chore(s.chore), forge.Failed.String(), note)
	fmt.Printf("    #%s  status=failed  note=%s\n", num, note)
}
