package settle

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/report"
)

// PromotionPolicy is the host-side auto-promotion gate for one Chore's
// findings (issue #3880, ADR 0056). It reaches ButlerSettle only through
// NewButlerSettle's caller -- never from the Box's issue-intent payload --
// so a Box cannot widen its own allow-list, raise its own file limit, or
// grant itself more of the day's promotion room; issueIntent.Class and
// .Concurrence are inputs to this gate, never the gate itself.
type PromotionPolicy struct {
	// Classes is this Chore's host-side finding-class allow-list (from
	// BUTLER_CHORE_CLASSES). Nil/empty means no class is promotable -- an
	// unconfigured Chore reads as opted out, not "trust the Box".
	Classes []string
	// MaxFiles is the host limit on how many files a promoted finding may
	// touch. A finding naming zero files is never promotable regardless of
	// MaxFiles (scope unknown), and MaxFiles itself must be >=1 for
	// anything to promote.
	MaxFiles int
	// Room reports how many promotions remain today; evaluated at most once
	// per Settle call (concurrent runs make a value fetched earlier stale by
	// the time this run would spend it). nil, or a func returning <=0,
	// means no room: promotion is off regardless of the other three gates.
	Room func() int
	// Label is the work kind's own configured dispatch label (LABEL,
	// Consumer-configurable) -- carried on a promoted finding alongside
	// "agent-butler-finding" so the work path picks it up (issue #3880).
	// Empty means unconfigured: never promote, rather than guess a name.
	Label string
}

// eligible reports whether in clears every promotion gate but room -- room
// is this call's shared, mutable per-sweep remaining counter, not p's Room
// field itself, so Settle checks it separately alongside eligible.
func (p PromotionPolicy) eligible(in issueIntent, files []string) bool {
	if p.Label == "" {
		return false
	}
	if in.Class == "" || !slices.Contains(p.Classes, in.Class) {
		return false
	}
	if len(files) < 1 || len(files) > p.MaxFiles {
		return false
	}
	return strings.TrimSpace(in.Concurrence) != ""
}

// ButlerSettle is the butler dispatch kind's one-shot settle adapter (ADR
// 0056, issue #3875): file each finding the Box reported, then write the
// Chore's Ledger done commit carrying the advanced lastSwept/cursor, the
// filed URLs, and the run's usage. No CI watch, no merge, and no tracker
// label transition -- the butler carries no tracker issue of its own, only
// a Ledger Chore.
type ButlerSettle struct {
	it                  forge.IssueTracker
	ledger              ledger.Backend
	chore               string
	claim               ledger.Tip
	scope               chore.Scope
	now                 func() time.Time
	maxFindingsPerSweep int
	policy              PromotionPolicy
}

var _ Settler = (*ButlerSettle)(nil)

// NewButlerSettle constructs a ButlerSettle for one Chore run. claim is the
// Ledger tip Claim produced at the start of this run -- ledger.Finish's
// compare-and-swap parent, unless Settle first reserves promotion slots
// (issue #3926), in which case the reservation commit takes over as parent;
// scope is the run's computed Scope
// (internal/chore.NextScope), whose Head/NextCursor become the done
// commit's lastSwept/cursor on success. maxFindingsPerSweep caps how many
// well-formed findings Settle will file in one sweep; 0 means no cap. policy
// is the host-side auto-promotion gate (issue #3880); its zero value
// (Classes nil, MaxFiles 0, Room nil) never promotes anything.
func NewButlerSettle(it forge.IssueTracker, backend ledger.Backend, choreName string, claim ledger.Tip, scope chore.Scope, now func() time.Time, maxFindingsPerSweep int, policy PromotionPolicy) *ButlerSettle {
	return &ButlerSettle{it: it, ledger: backend, chore: choreName, claim: claim, scope: scope, now: now, maxFindingsPerSweep: maxFindingsPerSweep, policy: policy}
}

// Settle files result's findings, if any, then writes the Chore's done
// Ledger commit. A crashed run -- no outcome line, or an outcome line whose
// status isn't "ready" (e.g. blocked) -- files nothing and writes no Ledger
// commit at all, so the claim stands and lastSwept/cursor stay put for the
// next run to resume from (ADR 0056).
func (b *ButlerSettle) Settle(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) {
	logRejectedSignals(num, result)
	if !result.Resolved.Found {
		b.fail(num, "no ready outcome line")
		return
	}
	o := result.Resolved.Outcome
	if o.Status != outcome.StatusReady {
		note := o.Note
		if note == "" {
			note = "status=" + o.Status
		}
		b.fail(num, note)
		return
	}

	kept, dropped := capIntents(result.IssueIntents, b.maxFindingsPerSweep)
	capped := result
	capped.IssueIntents = kept

	// Room is evaluated at most once per Settle, and only when this Chore
	// has an allow-list at all -- a Chore with no Classes can never promote,
	// so spending a Ledger walk on Room for it would be waste. Evaluating at
	// settle time rather than at run start means a promotion whose
	// reservation commit already landed is counted here; it is still a soft
	// cap like ADR 0056's other budgets, not a hard one -- two runs settling
	// at the same moment can each read the same total and both spend it.
	remaining := 0
	if len(b.policy.Classes) > 0 && b.policy.Room != nil {
		remaining = b.policy.Room()
	}

	// Reserve the slots this run intends to spend before filing anything
	// (issue #3926): if the done commit below never lands -- the claim was
	// lost to a takeover, or the push itself fails -- the reservation still
	// counts against DayTotals, so a lost Finish can never let a Chore
	// promote past the day's budget. finishParent moves to the reservation
	// tip on success so the done commit's own CAS is checked against it, not
	// the stale claim.
	finishParent := b.claim
	if remaining > 0 {
		eligible := 0
		for _, raw := range capped.IssueIntents {
			in, ok := parseIssueIntent(raw)
			if !ok {
				continue
			}
			if b.policy.eligible(in, butlerFiles(in.DedupTerms)) {
				eligible++
			}
		}
		n := min(remaining, eligible)
		if n > 0 {
			reserved, err := ledger.Reserve(b.ledger, b.chore, b.claim, n, b.now())
			if err != nil {
				fmt.Printf("    #%s  status=promotion-reserve-failed  !! %v\n", num, err)
				remaining = 0
			} else {
				remaining = n
				finishParent = reserved
			}
		}
	}

	filed := fileIssueIntentsDetailedFunc(b.it, num, capped, "agent-butler-finding", func(in issueIntent) (string, []string, func()) {
		backlink := butlerBacklink(b.chore, in)
		files := butlerFiles(in.DedupTerms)
		if !b.policy.eligible(in, files) || remaining <= 0 {
			return backlink, nil, nil
		}
		note := promotionNote(b.chore, in, b.policy, len(files))
		// Spend the slot only in onFiled, after PostIssue succeeds, so a
		// failed post frees it back to the rest of the sweep.
		return backlink + "\n\n" + note, []string{b.policy.Label}, func() { remaining-- }
	})
	reportFiled(num, filed)
	if dropped > 0 {
		fmt.Printf("    #%s  dropped %d finding(s) beyond the %d-per-sweep cap\n", num, dropped, b.maxFindingsPerSweep)
	}

	var urls, promoted []string
	for _, f := range filed {
		// Only a successful filing's URL is ever recorded: a Skipped intent
		// was never posted (there is nothing to name), and a Failed one's
		// dedup keys never reached the backlog, so leaving it out of Filed
		// lets a later rotation re-find and refile it -- recording it here
		// would suppress that retry for good.
		if f.Failed || f.Skipped {
			continue
		}
		urls = append(urls, f.URL)
		if b.policy.Label != "" && slices.Contains(f.ExtraLabels, b.policy.Label) {
			promoted = append(promoted, f.URL)
		}
	}

	state := ledger.State{
		LastSwept: b.scope.Head,
		Cursor:    b.scope.NextCursor,
		Filed:     urls,
		Promoted:  promoted,
		Usage:     d.CumulativeUsage(),
		Dropped:   dropped,
	}
	if _, err := ledger.Finish(b.ledger, b.chore, finishParent, state, b.now()); err != nil {
		fmt.Printf("    #%s  status=ledger-finish-failed  !! %v\n", num, err)
		report.Settled(dispatchkey.Chore(b.chore), forge.Failed.String(), fmt.Sprintf("ledger finish failed: %v", err))
		return
	}

	note := fmt.Sprintf("%d filed", len(urls))
	if len(promoted) > 0 {
		note = fmt.Sprintf("%d filed, %d promoted", len(urls), len(promoted))
	}
	if dropped > 0 {
		note = fmt.Sprintf("%s, %d dropped", note, dropped)
	}
	report.Settled(dispatchkey.Chore(b.chore), forge.Complete.String(), note)
	fmt.Printf("    #%s  status=%s  note=%s\n", num, o.Status, note)
}

// butlerBacklink renders the per-intent backlink fileIssueIntentsDetailedFunc
// appends to a filed finding's body: which Chore filed it, the Box-claimed
// class if any (issue #3870/#3880 -- named here regardless of whether the
// finding ends up promoted), and which files (the path before the first ':'
// in each dedup term, deduped, order kept) it concerns. The Files sentence is
// omitted entirely when terms yields no paths, rather than printing "Files: ".
func butlerBacklink(choreName string, in issueIntent) string {
	lead := fmt.Sprintf("Filed by the butler's `%s` Chore.", choreName)
	if in.Class != "" {
		lead += fmt.Sprintf(" Class: `%s`.", in.Class)
	}
	files := butlerFiles(in.DedupTerms)
	if len(files) == 0 {
		return lead
	}
	quoted := make([]string, len(files))
	for i, f := range files {
		quoted[i] = "`" + f + "`"
	}
	return fmt.Sprintf("%s Files: %s.", lead, strings.Join(quoted, ", "))
}

// promotionNote renders the visible note appended to a promoted finding's
// body, naming the class and quoting the reviewer's own words (issue #3880)
// rather than just asserting agreement -- a reader should be able to check
// the reviewer's claim, not just trust that it happened. Concurrence is
// sanitized here rather than trusted to the caller, collapsed to one line so
// an embedded newline can't break out of the note's sentence, and quoted in
// a code span (see sanitizeConcurrence). The span also leans on oneLine's
// trim (CommonMark strips a space opening/closing a span) and on eligible
// rejecting an empty Concurrence (a bare pair of backticks).
func promotionNote(choreName string, in issueIntent, policy PromotionPolicy, nFiles int) string {
	concurrence := oneLine(sanitizeConcurrence(in.Concurrence))
	return fmt.Sprintf(
		"**Auto-promoted** to `%s` by the butler: class `%s` is on the `%s` Chore's allow-list, it touches %d file(s) (host limit %d), and the in-Box reviewer agreed: `%s`",
		policy.Label, in.Class, choreName, nFiles, policy.MaxFiles, concurrence,
	)
}

// oneLine collapses s's internal whitespace, including any newline, down to
// single spaces and trims the ends -- Concurrence is Box-supplied reviewer
// prose (issue #3880) and must render as one line in host-authored note text.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// butlerFiles extracts the file path from each "path/to/file.go:Symbol"
// dedup term (the path before the first ':'), deduping while keeping first-
// seen order. A term with no ':' is taken whole, on the same "usable as-is"
// terms splitDedupTerms already applies elsewhere in this package.
func butlerFiles(dedupTerms []string) []string {
	seen := make(map[string]bool, len(dedupTerms))
	var files []string
	for _, term := range dedupTerms {
		path := term
		if i := strings.Index(term, ":"); i >= 0 {
			path = term[:i]
		}
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		files = append(files, path)
	}
	return files
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

// fail prints a status=failed line and reports the run failed. It applies no
// tracker transition: LabelsNone means the butler has no tracker issue to
// move, unlike ResearchSettle.fail's TransitionState call. Neither files
// findings nor writes a Ledger commit, so the claim stands and
// lastSwept/cursor stay at the prior run's values for the next run to
// resume from.
func (b *ButlerSettle) fail(num, note string) {
	report.Settled(dispatchkey.Chore(b.chore), forge.Failed.String(), note)
	fmt.Printf("    #%s  status=failed  note=%s\n", num, note)
}

// Fail is a no-op, reachable the same way ResearchSettle.Fail is: under
// CONTINUOUS_DISPATCH the caller already handles a Box exit itself and calls
// Fail on any Settler regardless of kind. The claim is left as-is -- the
// next run's takeover of a stale claim is Claim's job, not Settle's.
func (b *ButlerSettle) Fail(num string, gen uint64, result dispatch.Result) {
}
