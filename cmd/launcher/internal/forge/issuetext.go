package forge

import (
	"fmt"
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
}

// CommentLister is the optional IssueTracker interface for adapters that can
// list an issue's comments, discovered via type assertion. A local file-backed
// issue has no comment thread, so this stays outside IssueTracker.
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
func IssueText(t IssueTracker, num string) (string, error) {
	iss, err := t.Issue(num)
	if err != nil {
		return "", err
	}
	text := iss.Body
	if cl, ok := t.(CommentLister); ok {
		if comments, cErr := cl.Comments(num); cErr == nil && len(comments) > 0 {
			if len(comments) > issueTextCommentWindow {
				comments = comments[len(comments)-issueTextCommentWindow:]
			}
			var b strings.Builder
			b.WriteString(text)
			b.WriteString("\n\n## Comments\n\n")
			for _, c := range comments {
				fmt.Fprintf(&b, "%s (%s): %s\n", c.Author, c.CreatedAt, c.Body)
			}
			text = b.String()
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
	return text + renderLinkedIssues(links, maxIssueTextBytes-len(text)), nil
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
		var b strings.Builder
		b.WriteString("### Unresolved references\n\n")
		for i, l := range unresolved {
			if i > 0 {
				b.WriteByte('\n')
			}
			fmt.Fprintf(&b, "- %s (%s of %s): %s", l.Ref, l.Relation, l.LinkedFrom, l.Err)
		}
		unresolvedBlock = b.String()
		budget -= len(unresolvedBlock) + len(blockSep)
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

	var blocks []string
	var omittedRefs []string
	for i, l := range resolved {
		block := renderLinkedEntry(l)
		// Net cost of promoting this entry from its reserved omitted-list
		// line to a full rendered block.
		netCost := len(block) + len(blockSep) - (len(omittedLines[i]) + 1)
		if netCost <= entryBudget {
			entryBudget -= netCost
			blocks = append(blocks, block)
		} else {
			omittedRefs = append(omittedRefs, l.Ref)
		}
	}

	if len(unresolvedBlock) > 0 {
		blocks = append(blocks, unresolvedBlock)
	}
	if len(omittedRefs) > 0 {
		var b strings.Builder
		b.WriteString("### Omitted for size\n\n")
		for i, ref := range omittedRefs {
			if i > 0 {
				b.WriteByte('\n')
			}
			b.WriteString("- ")
			b.WriteString(ref)
		}
		blocks = append(blocks, b.String())
	}

	if len(blocks) == 0 {
		return ""
	}
	return linkedIssuesHeading + strings.Join(blocks, blockSep)
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
