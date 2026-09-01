package settle

import (
	"fmt"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// ResearchSettle is the research dispatch kind's one-shot settle adapter
// (ADR 0022): parse the outcome line, apply exactly one terminal label,
// done — no CI watch, no self-heal fix passes, no merge, no usage comment.
// Research lands no code, so there is nothing to adopt either.
type ResearchSettle struct {
	it forge.IssueTracker
	// landing, readOnly and filerEnabled are three independent reasons the
	// Box's verdict comment arrives as a relayed SPINDRIFT_COMMENT block on
	// stdout rather than a direct in-box issue comment, in which case Settle
	// must post it host-side and a missing block is fatal — nothing else ever
	// posts the verdict.
	//
	// landing is it's optional LandingRecorder surface (ADR 0029), non-nil
	// only for the local adapter, which doubles as the "is local" test (ADR
	// 0032): a local Box has no in-box tracker client at all.
	landing forge.LandingRecorder
	// readOnly mirrors BOX_FORGE_AND_ISSUE_ACCESS=read-only: the Box loses
	// its in-box write token. Driven by the mode directly rather than a
	// LandingRecorder-shaped type assertion github shouldn't need to
	// implement, and set via NewResearchSettleReadOnly rather than a Config
	// field — a one-shot research Settle has no other config to thread.
	readOnly bool
	// filerEnabled mirrors dispatchConfig's roster-derived Filer signal (ADR
	// 0041): research + Filer forces gates_tracker.go's researchForceRelay
	// unconditionally, so the relay applies even in read-write mode.
	filerEnabled bool
	// verdicts is the configured research verdict vocabulary (ADR 0022): the
	// ordered verdict->label set Settle validates the outcome's Status
	// against, from RESEARCH_VERDICTS or forge.ResearchVerdictLabels.
	verdicts forge.VerdictLabels
}

var _ Settler = (*ResearchSettle)(nil)

// NewResearchSettle constructs a ResearchSettle against it, the
// research-labeled IssueTracker instance (ADR 0022), for the
// BOX_FORGE_AND_ISSUE_ACCESS=read-write (default) path. See the struct fields
// for verdicts and filerEnabled.
func NewResearchSettle(it forge.IssueTracker, verdicts forge.VerdictLabels, filerEnabled bool) *ResearchSettle {
	landing, _ := it.(forge.LandingRecorder)
	return &ResearchSettle{it: it, landing: landing, verdicts: verdicts, filerEnabled: filerEnabled}
}

// NewResearchSettleReadOnly constructs a ResearchSettle for a Dispatch
// running under BOX_FORGE_AND_ISSUE_ACCESS=read-only: it posts the relayed
// SPINDRIFT_COMMENT via it.Comment before applying the verdict label, the
// same as the local path, because a read-only Box (github or not) has no
// in-box write token to post its own comment either.
func NewResearchSettleReadOnly(it forge.IssueTracker, verdicts forge.VerdictLabels, filerEnabled bool) *ResearchSettle {
	landing, _ := it.(forge.LandingRecorder)
	return &ResearchSettle{it: it, landing: landing, readOnly: true, verdicts: verdicts, filerEnabled: filerEnabled}
}

// Settle interprets result and drives num to its terminal research label: a
// parsed verdict applies CompleteVerdict; "blocked", an unparseable status,
// or a missing outcome line all mean the Box produced no usable verdict, so
// num transitions to Failed instead — crash-retry and verdict-review stay
// separate human queues. When the verdict comment travels the relay (see the
// struct fields), a missing or malformed block fails the same way.
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
	if result.CommentFound && result.Comment != "" {
		body := result.Comment
		if section := buildFiledIssuesSection(filed); section != "" {
			body = strings.TrimRight(body, "\n") + "\n\n" + section
		}
		// If filing above succeeded but this post fails, num is left in
		// agent-research-in-progress with issues already filed, and a retry
		// re-files them as duplicates (nothing here consults the intents'
		// dedupTerms). Known, accepted non-idempotency — the
		// file->comment->label order is what the spec requires.
		if err := r.it.Comment(num, body); err != nil {
			fmt.Printf("    #%s  status=comment-post-failed  !! %v\n", num, err)
			return
		}
	} else if r.landing != nil || r.readOnly || r.filerEnabled {
		r.fail(num, "no verdict comment block")
		return
	}
	if err := r.it.CompleteVerdict(num, verdict); err != nil {
		fmt.Printf("    #%s  landing=%s  status=verdict-apply-failed  !! %v\n", num, o.Landing, err)
		return
	}
	fmt.Printf("    #%s  landing=%s  status=%s  note=%s\n", num, o.Landing, o.Status, o.Note)
}

// buildFiledIssuesSection renders filed's successful entries as a "## Filed
// issues" Markdown list, degrading a failed entry to an inline "title —
// summary" bullet rather than dropping it: a filing failure is non-fatal but
// must still surface for a human to retry. Returns "" for an empty filed, so
// a comment carrying no intents is appended byte-for-byte unchanged.
//
// A successful entry only renders as a clickable link when f.URL looks like a
// real http(s) URL — the local tracker's PostIssue returns a "local:<slug>"
// identifier that would otherwise render as a dead link.
func buildFiledIssuesSection(filed []filedIntent) string {
	if len(filed) == 0 {
		return ""
	}
	lines := make([]string, 0, len(filed))
	for _, f := range filed {
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
	return "## Filed issues\n\n" + strings.Join(lines, "\n")
}

// firstLine returns s up to its first newline, trailing \r trimmed. A
// filing-failure bullet renders a finding's original Body inline in a single
// Markdown list item, and a multi-line body would break out of the bullet and
// inject arbitrary Markdown into the posted verdict comment. No ellipsis
// marker: the bullet reads as a summary and the full body is recoverable from
// the filed issue or the Box's own output.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSuffix(s, "\r")
}

// escapeMarkdownLinkText escapes s for use inside a Markdown link's text
// portion (or any bullet standing in for one): a title is agent-chosen text,
// and an unescaped `[` or `]` would break the surrounding syntax.
func escapeMarkdownLinkText(s string) string {
	s = strings.ReplaceAll(s, "[", "\\[")
	return strings.ReplaceAll(s, "]", "\\]")
}

// fail transitions num from InProgress to Failed (agent-research-failed),
// research's crash/no-verdict terminal.
func (r *ResearchSettle) fail(num, note string) {
	if err := r.it.TransitionState(num, forge.InProgress, forge.Failed); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not transition to failed: %v\n", num, err)
	}
	fmt.Printf("    #%s  status=failed  note=%s\n", num, note)
}

// Fail is a no-op today, but it is reachable: under CONTINUOUS_DISPATCH
// (e.g. dogfood.sh DOGFOOD_KIND=research), this Settler runs inside
// RunContinuous, whose Box-failure branch calls Fail on any Box exit. The
// empty body stays correct there too — the caller already transitions the
// tracker to Failed first — but don't skip calling Fail on the assumption
// it can't run.
func (r *ResearchSettle) Fail(num string, gen uint64, result dispatch.Result) {
}
