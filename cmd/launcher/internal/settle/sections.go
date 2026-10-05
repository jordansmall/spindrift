package settle

import (
	"fmt"
	"strings"
)

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
		title := markdownInlineText(f.Title)
		if f.Failed {
			lines = append(lines, fmt.Sprintf("- **%s** (filing failed) — %s", title, markdownInlineText(f.Body)))
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
		title := markdownInlineText(f.Title)
		ref := markdownInlineText(f.DupRef)
		lines = append(lines, fmt.Sprintf("- **%s** — already tracked: %s", title, ref))
	}
	if len(lines) == 0 {
		return ""
	}
	// "#123" can now be an open or a closed finding issue (issue #3873), and
	// `this run's "<title>"` matched a peer no backlog issue held at all --
	// the lead must read true for all three.
	lead := "These findings matched an already-filed issue — open, closed, or one this run filed itself — and were not filed again."
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

// firstLine truncates s at its first line break, LF or CR (CommonMark treats a
// lone CR as a line ending). A finding's Body can carry headings or fenced
// code, which rendered whole would break out of the Markdown bullet and inject
// arbitrary markup.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// markdownLinkTextEscaper escapes backslashes and brackets in a single pass,
// so the backslashes it adds before brackets are not re-escaped.
var markdownLinkTextEscaper = strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]")

// escapeMarkdownLinkText escapes the backslashes and brackets in s: an
// unescaped bracket breaks the surrounding Markdown syntax. A backslash is
// escaped too: left raw, one before a bracket would escape the escaping
// backslash and re-open the bracket as a live link (issue #4219).
func escapeMarkdownLinkText(s string) string {
	return markdownLinkTextEscaper.Replace(s)
}

// markdownInlineText renders an agent-authored value inside a Markdown bullet
// -- as link text, as bold text, as a bare dedup reference, or as a failed
// intent's body line: it cuts s at its first LF or CR (firstLine), then
// escapes its backslashes and brackets (escapeMarkdownLinkText).
func markdownInlineText(s string) string {
	return escapeMarkdownLinkText(firstLine(s))
}
