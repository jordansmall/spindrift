package forge

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

// maxIssueTextBytes bounds the string IssueText returns. The value reaches a
// Box through the runner's environment (offArgvKeys, issue #3470), and execve
// bounds each environment string as it bounds each argument, so IssueText
// truncates an unbounded comment thread. When t is also a LinkedIssueLister,
// the subject text and the rendered link chain share this one budget.
const maxIssueTextBytes = 64 * 1024

// issueTextCommentWindow bounds the trailing slice of comments IssueText
// renders, the last-10-comment snapshot every prompt fragment promises.
const issueTextCommentWindow = 10

// Comment is a single issue comment, normalized from an adapter's native shape.
type Comment struct {
	Author    string
	CreatedAt string
	Body      string
	// Minimized marks a comment a maintainer hid on the forge; IssueText leaves
	// it out of the transcript. Adapters that don't map moderation leave it
	// zero.
	Minimized       bool
	MinimizedReason string
	// Association is the author's GitHub author_association as the tracker
	// reports it (OWNER, MEMBER, COLLABORATOR, CONTRIBUTOR, NONE, ...), empty
	// when the tracker cannot attest the author's standing.
	Association string
}

// CommentTrusted reports whether c's author is OWNER, MEMBER or COLLABORATOR,
// the check #382's vetted-transcript filter will apply. An empty Association
// (the tracker cannot attest standing) is untrusted: fail closed. Rationale:
// docs/reference.md#threat-model.
func CommentTrusted(c Comment) bool {
	switch c.Association {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	}
	return false
}

// CommentLister is the optional IssueTracker interface for adapters that can
// list an issue's comments, discovered via type assertion. The local adapter
// inlines its launcher- and Box-authored thread into the body instead, so
// this stays outside IssueTracker.
type CommentLister interface {
	// Comments returns issue num's comments, oldest first, the order IssueText
	// assumes when it takes the trailing window of the slice.
	Comments(num string) ([]Comment, error)
}

// IssueText renders issue num's body, its last 10 comments under "## Comments"
// when t implements CommentLister, and num's transitive link chain under
// "## Linked issues" when t implements LinkedIssueLister. A t.Issue error is
// returned; a comments or LinkedIssues error is swallowed, so losing either
// section never fails a dispatch the subject text alone would have carried.
// Minimized comments are excluded before the window is taken, so hidden ones
// cannot evict live ones; each one dropped writes a line to warn.
func IssueText(t IssueTracker, num string, warn io.Writer) (string, error) {
	iss, err := t.Issue(num)
	if err != nil {
		return "", err
	}
	text := iss.Body
	if cl, ok := t.(CommentLister); ok {
		if comments, cErr := cl.Comments(num); cErr == nil {
			comments = dropMinimized(comments, num, warn)
			if len(comments) > issueTextCommentWindow {
				comments = comments[len(comments)-issueTextCommentWindow:]
			}
			if len(comments) > 0 {
				var b strings.Builder
				b.WriteString(text)
				b.WriteString("\n\n## Comments\n\n")
				for _, c := range comments {
					fmt.Fprintf(&b, "%s (%s): %s\n", c.Author, c.CreatedAt, c.Body)
				}
				text = b.String()
			}
		}
	}

	// No room left for a linked section, and truncation must keep the
	// marker-and-cut behavior it had before linked issues existed.
	if len(text) > maxIssueTextBytes {
		return truncateIssueText(text), nil
	}

	ll, ok := t.(LinkedIssueLister)
	if !ok {
		return text, nil
	}
	links, lErr := ll.LinkedIssues(num)
	if lErr != nil || len(links) == 0 {
		return text, nil
	}
	// Backstop: renderLinkedIssues sizes itself to the budget, but a future
	// sizing mistake should degrade to the visible marker, not an overshoot.
	return truncateIssueText(text + renderLinkedIssues(links, maxIssueTextBytes-len(text))), nil
}

const linkedIssuesHeading = "\n\n## Linked issues\n\n"

// blockSep separates each top-level block (a rendered entry, the unresolved
// list, the omitted list) inside the "## Linked issues" section.
const blockSep = "\n\n"

// relationRank orders LinkBlockedBy ahead of LinkParent: a direct blocker is
// what stops num from proceeding now, so it earns budget priority over an
// informational parent link.
func relationRank(r LinkRelation) int {
	if r == LinkBlockedBy {
		return 0
	}
	return 1
}

// renderLinkedIssues renders links as the body of a "## Linked issues" section
// within budget bytes. An entry too big to fit is omitted whole and listed by
// ref, never cut mid-body. Sizing reserves the heading, the unresolved list,
// and the worst case where every resolvable entry lands in the omitted list
// before spending on bodies, so the section can end under budget, never over.
// The unresolved list is sized first, then bodies, then the omitted list from
// whatever is left; a list that does not fit is cut line by line with the rest
// collapsed into an "and N more" line, or dropped if even that cannot fit.
func renderLinkedIssues(links []LinkedIssue, budget int) string {
	if budget <= len(linkedIssuesHeading) {
		return ""
	}
	budget -= len(linkedIssuesHeading)

	ordered := append([]LinkedIssue(nil), links...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Depth != ordered[j].Depth {
			return ordered[i].Depth < ordered[j].Depth
		}
		return relationRank(ordered[i].Relation) < relationRank(ordered[j].Relation)
	})

	var unresolved, resolved []LinkedIssue
	for _, l := range ordered {
		if l.Err != nil {
			unresolved = append(unresolved, l)
		} else {
			resolved = append(resolved, l)
		}
	}

	var unresolvedBlock string
	if len(unresolved) > 0 {
		lines := make([]string, len(unresolved))
		for i, l := range unresolved {
			lines[i] = fmt.Sprintf("- %s (%s of %s): %s", l.Ref, l.Relation, l.LinkedFrom, l.Err)
		}
		unresolvedBlock = boundedList("### Unresolved references\n\n", lines, budget-len(blockSep))
		if unresolvedBlock != "" {
			budget -= len(unresolvedBlock) + len(blockSep)
		}
	}

	// Reserve as if every resolvable entry ends up omitted, the worst case
	// admission can produce, before deciding which get rendered in full.
	omittedLines := make([]string, len(resolved))
	worstOmitted := 0
	if len(resolved) > 0 {
		worstOmitted += len("### Omitted for size\n\n") + len(blockSep)
	}
	for i, l := range resolved {
		omittedLines[i] = "- " + l.Ref
		worstOmitted += len(omittedLines[i]) + 1 // +1 for the '\n' joining bullets
	}
	entryBudget := budget - worstOmitted
	// remaining charges each admitted block in full, so what is left after
	// admission bounds the omitted list; entryBudget only gates admission.
	remaining := budget

	var blocks []string
	var omittedRefs []string
	for i, l := range resolved {
		block := renderLinkedEntry(l)
		// Net cost of promoting this entry from its reserved omitted-list
		// line to a full rendered block.
		netCost := len(block) + len(blockSep) - (len(omittedLines[i]) + 1)
		if netCost <= entryBudget {
			entryBudget -= netCost
			remaining -= len(block) + len(blockSep)
			blocks = append(blocks, block)
		} else {
			omittedRefs = append(omittedRefs, l.Ref)
		}
	}

	if unresolvedBlock != "" {
		blocks = append(blocks, unresolvedBlock)
	}
	if len(omittedRefs) > 0 {
		lines := make([]string, len(omittedRefs))
		for i, ref := range omittedRefs {
			lines[i] = "- " + ref
		}
		if omitted := boundedList("### Omitted for size\n\n", lines, remaining-len(blockSep)); omitted != "" {
			blocks = append(blocks, omitted)
		}
	}

	if len(blocks) == 0 {
		return ""
	}
	return linkedIssuesHeading + strings.Join(blocks, blockSep)
}

// boundedList renders heading plus lines joined by '\n', never longer than
// budget bytes. It keeps the longest prefix of lines that still leaves room for
// a "- … and N more" line counting the rest, and returns "" when no such
// prefix fits (not even a collapse line alone) or when lines is empty.
func boundedList(heading string, lines []string, budget int) string {
	n := len(lines)
	if n == 0 {
		return ""
	}
	render := func(k int) string {
		out := heading + strings.Join(lines[:k], "\n")
		if k < n {
			if k > 0 {
				out += "\n"
			}
			out += fmt.Sprintf("- … and %d more", n-k)
		}
		return out
	}
	if out := render(n); len(out) <= budget {
		return out
	}
	// Below n each kept line adds more bytes than the shrinking count saves,
	// so size grows with k and a binary search finds the longest fitting prefix.
	k := sort.Search(n, func(k int) bool { return len(render(k)) > budget })
	if k == 0 {
		return ""
	}
	return render(k - 1)
}

// renderLinkedEntry renders one resolved LinkedIssue as a heading, a status
// line built only from what the adapter recorded (nothing derived or invented),
// and the issue's body verbatim, which for the local adapter already carries
// its own "## Comments" section as plain body text.
func renderLinkedEntry(l LinkedIssue) string {
	var b strings.Builder
	b.WriteString("### ")
	b.WriteString(l.Ref)
	if l.Issue.Title != "" {
		b.WriteString(" — ")
		b.WriteString(l.Issue.Title)
	}
	fmt.Fprintf(&b, " (%s of %s)", l.Relation, l.LinkedFrom)

	b.WriteString("\n\nstatus: ")
	if l.Issue.State == IssueClosed {
		b.WriteString("closed")
	} else {
		b.WriteString("open")
	}
	if l.Issue.Landing != "" {
		b.WriteString(", landing: ")
		b.WriteString(l.Issue.Landing)
	}
	if l.Issue.Abandoned {
		b.WriteString(", abandoned")
	}

	if body := strings.TrimRight(l.Issue.Body, "\n"); body != "" {
		b.WriteString("\n\n")
		b.WriteString(body)
	}
	return b.String()
}

// truncateIssueText caps s at maxIssueTextBytes, cutting on a rune boundary so
// the tail cannot become a mangled half-rune, and appends a visible marker so
// an agent cannot mistake the cut for the issue simply having no more text.
func truncateIssueText(s string) string {
	if len(s) <= maxIssueTextBytes {
		return s
	}
	s = s[:maxIssueTextBytes]
	for len(s) > 0 {
		r, size := utf8.DecodeLastRuneInString(s)
		if r != utf8.RuneError || size > 1 {
			break
		}
		s = s[:len(s)-1]
	}
	return s + fmt.Sprintf("\n\n[truncated: issue text exceeded %dKB]\n", maxIssueTextBytes/1024)
}

// dropMinimized returns comments without the minimized ones, in order, writing
// one line per drop to warn. It copies rather than filtering in place, so the
// adapter's slice is left untouched.
func dropMinimized(comments []Comment, num string, warn io.Writer) []Comment {
	live := make([]Comment, 0, len(comments))
	for _, c := range comments {
		if c.Minimized {
			reason := c.MinimizedReason
			if reason == "" {
				reason = "no reason"
			}
			fmt.Fprintf(warn, "    ?? #%s: dropped minimized comment by %s (%s)\n", num, c.Author, reason)
			continue
		}
		live = append(live, c)
	}
	return live
}
