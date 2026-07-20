package settle

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// ResearchSettle is the research dispatch kind's one-shot settle adapter
// (ADR 0022): parse the outcome line, apply exactly one terminal label,
// done — no CI watch, no self-heal fix passes, no merge, no usage comment.
// Modeled on the push-only (PRForge-absent) branch of the work Settle, but
// leaner still: research lands no code, so there is nothing to adopt either.
type ResearchSettle struct {
	it forge.IssueTracker
	// landing is it's optional LandingRecorder surface (ADR 0029), resolved
	// once at construction via a type assertion — non-nil only for the
	// local adapter (github/jira don't implement it). Doubles as this
	// Settle's "is local" test (ADR 0032, issue #1692): a local Dispatch's
	// Box has no in-box tracker client, so its verdict comment travels as a
	// SPINDRIFT_COMMENT block on stdout instead of a direct gh issue
	// comment, and this Settle posts it host-side before applying the
	// verdict label.
	landing forge.LandingRecorder
}

var _ Settler = (*ResearchSettle)(nil)

// NewResearchSettle constructs a ResearchSettle against it, the
// research-labeled IssueTracker instance (ADR 0022's fixed
// agent-research/agent-research-in-progress/verdict label family).
func NewResearchSettle(it forge.IssueTracker) *ResearchSettle {
	landing, _ := it.(forge.LandingRecorder)
	return &ResearchSettle{it: it, landing: landing}
}

// Settle interprets result and drives num to its terminal research label:
// a parsed verdict (recommend/reject/unclear) applies CompleteVerdict;
// "blocked", an unparseable status, or a missing outcome line all mean the
// Box produced no usable verdict, so num is transitioned to Failed
// (agent-research-failed) instead — crash-retry and verdict-review stay
// separate human queues. For a local tracker (ADR 0032, issue #1692), a
// verdict outcome additionally requires a complete SPINDRIFT_COMMENT block —
// posted host-side via Comment before the verdict label is applied — a
// missing or malformed block is treated the same as a missing outcome line.
func (r *ResearchSettle) Settle(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) {
	if !result.OutcomeFound {
		r.fail(num, "no verdict outcome line")
		return
	}
	o := result.Outcome
	verdict, ok := forge.ParseVerdict(o.Status)
	if !ok {
		r.fail(num, o.Note)
		return
	}
	if r.landing != nil {
		if !result.CommentFound || result.Comment == "" {
			r.fail(num, "no verdict comment block")
			return
		}
		if err := r.it.Comment(num, result.Comment); err != nil {
			fmt.Printf("    #%s  status=comment-post-failed  !! %v\n", num, err)
			return
		}
	}
	if err := r.it.CompleteVerdict(num, verdict); err != nil {
		fmt.Printf("    #%s  landing=%s  status=verdict-apply-failed  !! %v\n", num, o.Landing, err)
		return
	}
	fmt.Printf("    #%s  landing=%s  status=%s  note=%s\n", num, o.Landing, o.Status, o.Note)
}

// fail transitions num from InProgress to Failed (agent-research-failed),
// research's crash/no-verdict terminal.
func (r *ResearchSettle) fail(num, note string) {
	if err := r.it.TransitionState(num, forge.InProgress, forge.Failed); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not transition to failed: %v\n", num, err)
	}
	fmt.Printf("    #%s  status=failed  note=%s\n", num, note)
}

// SettleAdopted is unreachable in practice: research never opens a PR, so
// there is no already-discovered PR to adopt. Present only to satisfy the
// Settler interface.
func (r *ResearchSettle) SettleAdopted(d dispatch.Dispatcher, num string, gen uint64, prURL string) {
}

// Fail is a no-op today, but it is reachable: under CONTINUOUS_DISPATCH
// (e.g. dogfood.sh DOGFOOD_KIND=research), this Settler runs inside
// RunContinuous, whose Box-failure branch calls Fail on any Box exit. The
// empty body stays correct there too — the caller already transitions the
// tracker to Failed first — but don't skip calling Fail on the assumption
// it can't run.
func (r *ResearchSettle) Fail(num string, gen uint64, result dispatch.Result) {
}
