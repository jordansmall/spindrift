package settle

import (
	"fmt"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/report"
)

// ResearchSettle is the research dispatch kind's one-shot settle adapter
// (ADR 0022): parse the outcome line, apply one terminal label, stop. No CI
// watch, no fix passes, no merge, no usage comment.
type ResearchSettle struct {
	it forge.IssueTracker
	// landing is it's optional LandingRecorder (ADR 0029), non-nil only for the
	// local adapter, so it doubles as this Settle's "is local" test (ADR 0032,
	// issue #1692): a local Box has no in-box tracker client, so its verdict
	// comment arrives as a SPINDRIFT_COMMENT block this Settle posts host-side.
	landing forge.LandingRecorder
	// readOnly mirrors BOX_FORGE_AND_ISSUE_ACCESS=read-only (issue #1917): the
	// Box has no in-box write token, so its verdict comment travels the same
	// SPINDRIFT_COMMENT relay. The mode drives this rather than a type
	// assertion, because github implements no LandingRecorder.
	readOnly bool
	// filerEnabled mirrors dispatchConfig's roster-derived Filer signal (issue
	// #2593, ADR 0041): research plus the Filer forces gates_tracker.go's
	// researchForceRelay even in read-write mode, so a missing relayed comment
	// must fail here rather than fall through to CompleteVerdict.
	filerEnabled bool
	// verdicts is the configured verdict-to-label vocabulary (ADR 0022, issue
	// #2201) Settle validates the posted Status against, from RESEARCH_VERDICTS
	// or the compiled forge.ResearchVerdictLabels default.
	verdicts forge.VerdictLabels
}

var _ Settler = (*ResearchSettle)(nil)

// NewResearchSettle constructs a ResearchSettle for the read-write (default)
// BOX_FORGE_AND_ISSUE_ACCESS path. filerEnabled forces the SPINDRIFT_COMMENT
// relay even here, so a missing verdict comment must fail (issue #2593).
func NewResearchSettle(it forge.IssueTracker, verdicts forge.VerdictLabels, filerEnabled bool) *ResearchSettle {
	landing, _ := it.(forge.LandingRecorder)
	return &ResearchSettle{it: it, landing: landing, verdicts: verdicts, filerEnabled: filerEnabled}
}

// NewResearchSettleReadOnly constructs a ResearchSettle for a Dispatch under
// BOX_FORGE_AND_ISSUE_ACCESS=read-only (issue #1917): the Box has no write
// token, so this Settle posts the relayed comment before the verdict label.
func NewResearchSettleReadOnly(it forge.IssueTracker, verdicts forge.VerdictLabels, filerEnabled bool) *ResearchSettle {
	landing, _ := it.(forge.LandingRecorder)
	return &ResearchSettle{it: it, landing: landing, readOnly: true, verdicts: verdicts, filerEnabled: filerEnabled}
}

// Settle drives num to its terminal research label: a parsed verdict applies
// CompleteVerdict, while a blocked, unparseable, or missing outcome line
// transitions num to Failed so crash-retry and verdict-review stay separate
// human queues. Under the comment relay a missing SPINDRIFT_COMMENT block
// counts as a missing outcome line.
func (r *ResearchSettle) Settle(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) {
	logRejectedSignals(num, result)
	if !result.Resolved.Found {
		r.fail(num, "no verdict outcome line")
		return
	}
	o := result.Resolved.Outcome
	verdict, ok := r.verdicts.Parse(o.Status)
	if !ok {
		r.fail(num, o.Note)
		return
	}
	backlink := fmt.Sprintf("Filed from research on #%s", num)
	filed := fileIssueIntentsDetailed(r.it, num, result, "agent-research-finding", backlink)
	// Reported right after filing, not at the function's tail: the comment-post
	// and verdict-apply branches below both return early, and filing has already
	// happened by then, so the tally must still print (issue #3608).
	reportFiled(num, filed)
	// Comment != "" is redundant now that parseSignalLine rejects a
	// zero-length payload (issue #3668); kept because the failure it guards is
	// user-visible -- an empty verdict comment on a human's tracker issue --
	// and the else branch is the safer fallback if that invariant regresses.
	if result.CommentFound && result.Comment != "" {
		body := result.Comment
		if sections := buildVerdictCommentSections(filed); sections != "" {
			body = strings.TrimRight(body, "\n") + "\n\n" + sections
		}
		if err := r.it.Comment(num, body); err != nil {
			fmt.Printf("    #%s  status=comment-post-failed  !! %v\n", num, err)
			return
		}
	} else if r.landing != nil || r.readOnly || r.filerEnabled {
		r.fail(num, commentFailureNote(result.CommentRejected))
		return
	}
	if err := r.it.CompleteVerdict(num, verdict); err != nil {
		fmt.Printf("    #%s  landing=%s  status=verdict-apply-failed  !! %v\n", num, o.Landing, err)
		return
	}
	// State stays DispatchState-only (forge.Complete); the verdict itself
	// (recommend/reject/unclear, ADR 0022) rides in the note, since the report
	// protocol's state field is not research-verdict vocabulary.
	//
	// Emitted inline rather than through gate.go's transitionState/flushSettled
	// latch: that latch arbitrates a dispatch settle that reaches a terminal
	// state twice for one issue (completeLanding's Complete, then
	// verifyMerged's demotion). Every ResearchSettle path reaches exactly one,
	// so inline is already "once".
	report.Settled(num, forge.Complete.String(), "verdict "+string(verdict))
	fmt.Printf("    #%s  landing=%s  status=%s  note=%s\n", num, o.Landing, o.Status, o.Note)
}

// buildFiledIssuesSection renders filed's entries as a "## Filed issues"
// Markdown list, and returns "" for an empty filed. A failed filing degrades to
// a bullet so a human notices and retries it; so does a non-http(s) URL (the
// local tracker returns local:<slug>), which would otherwise be a dead link.
func buildFiledIssuesSection(filed []filedIntent) string {
	lines := make([]string, 0, len(filed))
	for _, f := range filed {
		// A skip has no URL and no failure Body to render -- it was never
		// posted at all (issue #3609) -- so it renders no bullet.
		if f.Skipped {
			continue
		}
		title := escapeMarkdownLinkText(f.Title)
		if f.Failed {
			lines = append(lines, fmt.Sprintf("- **%s** (filing failed) — %s", title, firstLine(f.Body)))
			continue
		}
		if strings.HasPrefix(f.URL, "http://") || strings.HasPrefix(f.URL, "https://") {
			lines = append(lines, fmt.Sprintf("- [%s](%s)", title, f.URL))
			continue
		}
		lines = append(lines, fmt.Sprintf("- **%s** — %s", title, f.URL))
	}
	if len(lines) == 0 {
		return ""
	}
	return "## Filed issues\n\n" + strings.Join(lines, "\n")
}

// buildSkippedIssuesSection renders filed's Skipped entries as a "## Skipped
// (deduplicated)" Markdown list, and returns "" when none are skipped. A
// skip is otherwise invisible in the verdict comment -- stdout gets the
// "skipped duplicate" line, the comment does not -- so a human reading an
// all-dedup run's comment can't tell "deduplicated" from "never filed"
// (issue #3811). It is its own section rather than a bullet under "Filed
// issues": nothing here was filed, so listing it there would misreport.
func buildSkippedIssuesSection(filed []filedIntent) string {
	lines := make([]string, 0, len(filed))
	for _, f := range filed {
		if !f.Skipped {
			continue
		}
		title := escapeMarkdownLinkText(firstLine(f.Title))
		ref := escapeMarkdownLinkText(f.DupRef)
		lines = append(lines, fmt.Sprintf("- **%s** — already tracked: %s", title, ref))
	}
	if len(lines) == 0 {
		return ""
	}
	// Both DupRef kinds have to read true here: "#123" is a backlog match, but
	// `this run's "<title>"` matched a peer no open backlog held.
	lead := "These findings matched an already-filed issue — in the open backlog, or one this run filed itself — and were not filed again."
	return "## Skipped (deduplicated)\n\n" + lead + "\n\n" + strings.Join(lines, "\n")
}

// buildVerdictCommentSections joins the filed and skipped renderers, in that
// order, blank-line separated, for the verdict-comment call site to append
// as one block. Returns "" when neither has anything to render.
func buildVerdictCommentSections(filed []filedIntent) string {
	sections := make([]string, 0, 2)
	if s := buildFiledIssuesSection(filed); s != "" {
		sections = append(sections, s)
	}
	if s := buildSkippedIssuesSection(filed); s != "" {
		sections = append(sections, s)
	}
	return strings.Join(sections, "\n\n")
}

// firstLine truncates s at its first newline and trims a trailing carriage
// return. A finding's Body can carry headings or fenced code, which rendered
// whole would break out of the Markdown bullet and inject arbitrary markup.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSuffix(s, "\r")
}

// escapeMarkdownLinkText escapes s for rendering inside a Markdown bullet --
// as link text, as bold text, or as a bare dedup reference: each of those is
// agent-chosen, and an unescaped bracket breaks the surrounding syntax.
func escapeMarkdownLinkText(s string) string {
	s = strings.ReplaceAll(s, "[", "\\[")
	return strings.ReplaceAll(s, "]", "\\]")
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
func (r *ResearchSettle) fail(num, note string) {
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
	report.Settled(num, forge.Failed.String(), note)
	fmt.Printf("    #%s  status=failed  note=%s\n", num, note)
}

// Fail is a no-op but is reachable: under CONTINUOUS_DISPATCH this Settler runs
// inside RunContinuous, whose Box-failure branch calls Fail on any Box exit. The
// empty body is correct because the caller already transitions the tracker to
// Failed, so do not skip calling Fail on the assumption it cannot run.
func (r *ResearchSettle) Fail(num string, gen uint64, result dispatch.Result) {
}
