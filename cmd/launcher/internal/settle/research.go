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
	// readOnly mirrors BOX_FORGE_AND_ISSUE_ACCESS=read-only (issue #1917):
	// a github (or jira) Dispatch's Box loses its in-box write token under
	// read-only mode, so its verdict comment travels the same
	// SPINDRIFT_COMMENT relay local's landing != nil case always gets —
	// driven directly by the mode, not by a LandingRecorder-shaped type
	// assertion github doesn't (and shouldn't need to) implement. Set via
	// the dedicated NewResearchSettleReadOnly constructor below rather than
	// a Config field like Settle.readOnly (settle.go): a one-shot research
	// Settle has no other config to thread, so a second constructor reads
	// clearer at each of its two call sites than a single-field Config
	// would.
	readOnly bool
	// filerEnabled mirrors the same roster-derived signal
	// dispatchConfig/resolveAgentPresenceSignals resolves for the Filer
	// (issue #2593, ADR 0041): a research dispatch with the Filer
	// provisioned forces gates_tracker.go's researchForceRelay
	// unconditionally, even in read-write mode, so the Box's Filer-relay
	// research-verdict-github-readonly.md fragment (and its
	// SPINDRIFT_COMMENT-only posting instruction) renders regardless of
	// readOnly. A missing/empty relayed comment under that combo is exactly
	// as unrecoverable as the read-only/local case already guards below --
	// nothing else ever posts the verdict -- so filerEnabled joins landing
	// != nil and readOnly as a third reason a missing comment must fail
	// instead of silently falling through to CompleteVerdict.
	filerEnabled bool
	// verdicts is the configured research verdict vocabulary (ADR 0022,
	// issue #2201): the ordered verdict->label set Settle validates the
	// posted outcome's Status against, sourced from RESEARCH_VERDICTS (or
	// the compiled default, forge.ResearchVerdictLabels, when unset) via
	// main.go's researchVerdictLabels(c) at construction time.
	verdicts forge.VerdictLabels
}

var _ Settler = (*ResearchSettle)(nil)

// NewResearchSettle constructs a ResearchSettle against it, the
// research-labeled IssueTracker instance (ADR 0022's fixed
// agent-research/agent-research-in-progress state-label family plus the
// configured verdict vocabulary), for the BOX_FORGE_AND_ISSUE_ACCESS=read-write
// (default) path. verdicts is the configured research verdict set (ADR 0022,
// issue #2201) Settle validates the posted verdict against — the compiled
// default (forge.ResearchVerdictLabels) unless RESEARCH_VERDICTS overrides it.
// filerEnabled mirrors the same roster-derived signal dispatchConfig passes
// as FilerEnabled (issue #2593, ADR 0041): research + Filer forces the
// SPINDRIFT_COMMENT relay unconditionally, even here in the read-write path,
// so a missing verdict comment must fail exactly like the read-only/local
// case does today rather than silently applying the verdict label with no
// comment ever posted.
func NewResearchSettle(it forge.IssueTracker, verdicts forge.VerdictLabels, filerEnabled bool) *ResearchSettle {
	landing, _ := it.(forge.LandingRecorder)
	return &ResearchSettle{it: it, landing: landing, verdicts: verdicts, filerEnabled: filerEnabled}
}

// NewResearchSettleReadOnly constructs a ResearchSettle for a Dispatch
// running under BOX_FORGE_AND_ISSUE_ACCESS=read-only (issue #1917): it posts
// the relayed SPINDRIFT_COMMENT via it.Comment before applying the verdict
// label, the same as NewResearchSettle already does for a LandingRecorder-
// implementing (local) tracker — because the read-only Box, github or not,
// has no in-box write token to post its own comment with either. verdicts is
// the configured research verdict set (ADR 0022, issue #2201) Settle
// validates the posted verdict against — the compiled default
// (forge.ResearchVerdictLabels) unless RESEARCH_VERDICTS overrides it.
// filerEnabled mirrors the same roster-derived signal dispatchConfig passes
// as FilerEnabled (issue #2593, ADR 0041): research + Filer forces the
// SPINDRIFT_COMMENT relay unconditionally regardless of read-only/read-write,
// so a missing verdict comment must fail here too, same reasoning as
// NewResearchSettle's own filerEnabled doc.
func NewResearchSettleReadOnly(it forge.IssueTracker, verdicts forge.VerdictLabels, filerEnabled bool) *ResearchSettle {
	landing, _ := it.(forge.LandingRecorder)
	return &ResearchSettle{it: it, landing: landing, readOnly: true, verdicts: verdicts, filerEnabled: filerEnabled}
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
		// If filing above already succeeded (filed is non-empty with
		// successes) but this post then fails, we return here without
		// applying a verdict label and without transitioning to Failed —
		// num is left in agent-research-in-progress with issues already
		// filed. A retry (re-applying agent-research) re-files them,
		// producing duplicates, since nothing here consults the intents'
		// own dedupTerms. Known, accepted non-idempotency (the
		// file->comment->label order is what the spec requires), not an
		// oversight.
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
// issues" Markdown list linking each real URL, and degrades a failed entry
// to an inline "title — summary" bullet in the same section (no URL to
// link) rather than silently dropping it — a filing failure is non-fatal
// and must still surface for a human to notice and retry. Returns "" for an
// empty filed, so a comment carrying no intents is appended byte-for-byte
// unchanged.
//
// Every title (success or failed) is Markdown-link-escaped before
// rendering — f.Title is agent-chosen text that could otherwise contain `[`
// or `]` and break the surrounding syntax. A successful entry only renders
// as a clickable `[title](url)` link when f.URL looks like a real http(s)
// URL; the local tracker's PostIssue returns a "local:<slug>" identifier
// rather than a URL, which would otherwise render as a dead link, so that
// (and any other non-http(s) URL) instead renders as a plain "title — url"
// bullet, the same shape a failed filing uses.
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

// firstLine returns s truncated to the text up to (not including) its first
// newline, with any trailing \r trimmed — a filing-failure bullet renders a
// finding's full original Body (which may be multi-line, with headings or
// fenced code) inline in a single Markdown list item, and an unescaped
// multi-line body would break out of the bullet and inject arbitrary
// Markdown into the posted verdict comment. No ellipsis marker: the bullet
// already reads as a summary, and the full body is either what got filed to
// the issue itself (filing succeeded elsewhere in the same run) or is
// otherwise recoverable from the Box's own output.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSuffix(s, "\r")
}

// escapeMarkdownLinkText escapes s for safe use inside a Markdown link's
// text portion (or any bullet standing in for one): f.Title is agent-chosen
// text, and an unescaped `[` or `]` would break the surrounding
// `[title](url)` or bullet syntax.
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
