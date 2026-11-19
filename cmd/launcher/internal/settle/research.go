package settle

import (
	"fmt"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

// ResearchSettle is the research dispatch kind's one-shot settle adapter
// (ADR 0022): parse the outcome line, apply one terminal label, stop. No CI
// watch, no fix passes, no merge, no usage comment.
type ResearchSettle struct {
	progress Progress
	it       forge.IssueTracker
	// findingLabel is the label a filed research finding wears, read from the
	// dispatched kind's row.
	findingLabel string
	// verdicts is the configured verdict-to-label vocabulary (ADR 0022, issue
	// #2201) Settle validates the posted Status against, from RESEARCH_VERDICTS
	// or the compiled forge.ResearchVerdictLabels default.
	verdicts forge.VerdictLabels
}

var _ Settler = (*ResearchSettle)(nil)

// NewResearchSettle constructs a ResearchSettle. The Box holds no write token
// and relays its verdict comment as a SPINDRIFT_COMMENT block, so this Settle
// posts it before the verdict label.
func NewResearchSettle(it forge.IssueTracker, verdicts forge.VerdictLabels, findingLabel string) *ResearchSettle {
	return &ResearchSettle{it: it, progress: StdoutProgress{}, findingLabel: findingLabel, verdicts: verdicts}
}

// WithProgress sends r's progress steps to p instead of stdout. Returns r so a
// caller can chain it onto the constructor.
func (r *ResearchSettle) WithProgress(p Progress) *ResearchSettle {
	r.progress = p
	return r
}

// Settle drives num to its terminal research label: a parsed verdict applies
// CompleteVerdict, while a blocked, unparseable, or missing outcome line
// transitions num to Failed so crash-retry and verdict-review stay separate
// human queues. Under the comment relay a missing SPINDRIFT_COMMENT block
// counts as a missing outcome line.
func (r *ResearchSettle) Settle(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) Settlement {
	RecordSettleWarnings(d, num, "", result)
	if !result.Resolved.Found {
		return r.fail(d, num, "no verdict outcome line")
	}
	o := result.Resolved.Outcome
	verdict, ok := r.verdicts.Parse(o.Status)
	if !ok {
		return r.fail(d, num, o.Note)
	}
	backlink := fmt.Sprintf("Filed from research on #%s", num)
	filed := fileIssueIntentsDetailed(r.progress, r.it, num, result, r.findingLabel, backlink)
	// Reported right after filing, not at the function's tail: the comment-post
	// and verdict-apply branches below both return early, and filing has already
	// happened by then, so the tally must still print (issue #3608).
	reportFiled(r.progress, num, filed)
	// Comment != "" is redundant now that both carriers reject a zero-length
	// payload -- outcome's classifySignalAt on the log (issue #3668) and
	// signalwire.CheckFields on the socket; kept because the failure it guards is
	// user-visible -- an empty verdict comment on a human's tracker issue --
	// and the else branch is the safer fallback if that invariant regresses.
	if result.CommentFound && result.Comment != "" {
		body := result.Comment
		if sections := buildVerdictCommentSections(filed); sections != "" {
			body = strings.TrimRight(body, "\n") + "\n\n" + sections
		}
		if err := r.it.Comment(num, body); err != nil {
			r.progress.Step(Step{Key: num, Status: StatusCommentPostFailed, Detail: fmt.Sprintf("!! %v", err)})
			return Settlement{}
		}
	} else {
		return r.fail(d, num, commentFailureNote(result.CommentRejected))
	}
	if err := r.it.CompleteVerdict(num, verdict); err != nil {
		r.progress.Step(Step{Key: num, Landing: o.Landing, Status: StatusVerdictApplyFailed, Detail: fmt.Sprintf("!! %v", err)})
		return Settlement{}
	}
	// State stays DispatchState-only (forge.Complete); the verdict itself
	// (recommend/reject/unclear, ADR 0022) rides in the note, since the report
	// protocol's state field is not research-verdict vocabulary.
	//
	// Settled inline rather than through gate.go's transitionState/flushSettled
	// latch: that latch arbitrates a dispatch settle that reaches a terminal
	// state twice for one issue (completeLanding's Complete, then
	// verifyMerged's demotion). Every ResearchSettle path reaches exactly one,
	// so inline is already "once".
	st := SettledBy(d, dispatchkey.Issue(num), ForReason(dispatchrecord.ReasonVerdict), "verdict "+string(verdict))
	r.progress.Step(Step{Key: num, Landing: o.Landing, Status: Status(o.Status), Detail: "note=" + o.Note})
	return st
}

// commentFailureNote distinguishes "the Box never emitted a comment line" from
// "it emitted one the host could not decode" (issue #3670). Both used to read
// "no verdict comment block", which sent a human triaging agent-research-failed
// looking for a comment that had in fact been sent and cut at the Box's Bash
// output cap (runs #3595, #3597).
func commentFailureNote(rejected outcome.Rejections) string {
	if rejected.Total() == 0 {
		return "no verdict comment block"
	}
	return "verdict comment block found but unreadable: " + rejected.Detail()
}

// fail transitions num from InProgress to Failed (agent-research-failed).
func (r *ResearchSettle) fail(d dispatch.Dispatcher, num, note string) Settlement {
	if err := r.it.TransitionState(num, forge.InProgress, forge.Failed); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not transition to failed: %v\n", num, err)
	}
	// note is the one piece of this failure a human triaging the report
	// stream needs verbatim: which reason (no verdict line, unparseable
	// status, missing comment relay) landed this issue in agent-research-failed.
	//
	// Inline, not through the transitionState/flushSettled latch (see the
	// comment at ResearchSettle's other Settled call above): this path also
	// reaches exactly one terminal state per issue.
	st := SettledBy(d, dispatchkey.Issue(num), ForReason(dispatchrecord.ReasonResearchFailed), note)
	r.progress.Step(Step{Key: num, Status: StatusFailed, Detail: "note=" + note})
	return st
}
