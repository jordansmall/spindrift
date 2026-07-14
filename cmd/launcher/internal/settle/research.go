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
}

var _ Settler = (*ResearchSettle)(nil)

// NewResearchSettle constructs a ResearchSettle against it, the
// research-labeled IssueTracker instance (ADR 0022's fixed
// agent-research/agent-research-in-progress/verdict label family).
func NewResearchSettle(it forge.IssueTracker) *ResearchSettle {
	return &ResearchSettle{it: it}
}

// Settle interprets result and drives num to its terminal research label:
// a parsed verdict (recommend/reject/unclear) applies CompleteVerdict;
// "blocked", an unparseable status, or a missing outcome line all mean the
// Box produced no usable verdict, so num is transitioned to Failed
// (agent-research-failed) instead — crash-retry and verdict-review stay
// separate human queues.
func (r *ResearchSettle) Settle(d dispatch.Dispatcher, num string, result dispatch.Result) {
	if !result.OutcomeFound {
		r.fail(num, "no verdict outcome line")
		return
	}
	o := result.Outcome
	if verdict, ok := forge.ParseVerdict(o.Status); ok {
		if err := r.it.CompleteVerdict(num, verdict); err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: could not apply verdict %s: %v\n", num, verdict, err)
		}
		fmt.Printf("    #%s  landing=%s  status=%s  note=%s\n", num, o.Landing, o.Status, o.Note)
		return
	}
	r.fail(num, o.Note)
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
func (r *ResearchSettle) SettleAdopted(d dispatch.Dispatcher, num, prURL string) {
}
