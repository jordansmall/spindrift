package forge

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// maxIssueTextBytes bounds the string IssueText returns. An issue thread
// with an unbounded comment count has no length limit of its own, but the
// value travels into a Box through the runner process's own environment on
// both routes (offArgvKeys in cmd/launcher/internal/runner: resolvedRunEnv
// under bwrap, ociRunEnv under OCI; issue #3470), and
// execve bounds each environment string the same way it bounds each
// argument -- so IssueText truncates rather than letting a pathological
// thread blow past that limit. When t is also a LinkedIssueLister, this is
// one shared budget for the subject text plus the whole rendered link
// chain, not a separate allowance per section -- see IssueText.
const maxIssueTextBytes = 64 * 1024

// issueTextCommentWindow bounds the trailing slice of comments IssueText
// renders -- the "last-10-comment snapshot" every prompt fragment promises.
const issueTextCommentWindow = 10

// Comment is a single issue comment, normalized from an adapter's native
// shape into the three fields IssueText renders.
type Comment struct {
	Author    string
	CreatedAt string
	Body      string
}

// CommentLister is the optional IssueTracker surface for adapters that can
// list an issue's comments. Discovered via type assertion, like
// BlockersLister and the other optional IssueTracker surfaces above: not
// every adapter needs one (a local file-backed issue has no comment
// thread), so this stays outside the required IssueTracker interface rather
// than forcing every implementation to grow a stub.
type CommentLister interface {
	// Comments returns issue num's comments, oldest first -- the order
	// IssueText assumes when it takes the trailing window of the slice.
	Comments(num string) ([]Comment, error)
}

// IssueText renders issue num's body, plus -- when t also implements
// CommentLister -- its last 10 comments under a "## Comments" heading, each
// rendered "author (createdAt): body". This mirrors the snapshot shape the
// prompt fragments used to prescribe via
// `.comments[-10:][] | "\(.author.login) (\(.createdAt)): \(.body)"`, now
// resolved host-side instead of in-box.
//
// When t also implements LinkedIssueLister and num has at least one linked
// issue, a "## Linked issues" section follows the subject text, rendering
// num's transitive "## Blocked by" and parent: chain (see LinkedIssueLister
// and renderLinkedIssues) within what's left of the shared maxIssueTextBytes
// budget. A remote tracker (github, jira) is never a LinkedIssueLister, so
// its output is untouched by this: byte-identical to before this feature.
//
// A t.Issue error is returned -- there is no text to build without it. A
// comments-fetch error, and a LinkedIssues error, are both deliberately
// swallowed: each degrades to the text built so far rather than failing
// IssueText, since losing the comment snapshot or the link chain must never
// fail a dispatch that would otherwise have proceeded on the subject alone.
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

	// The subject alone already exceeds the budget: there's no room left
	// for a linked section, and truncateIssueText's marker-and-cut behavior
	// must stay exactly what it was before this feature existed.
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

// linkedIssuesHeading opens the "## Linked issues" section renderLinkedIssues
// appends to the subject text.
const linkedIssuesHeading = "\n\n## Linked issues\n\n"

// blockSep separates each top-level block (a rendered entry, the unresolved
// list, the omitted list) inside the "## Linked issues" section.
const blockSep = "\n\n"

// relationRank orders LinkBlockedBy ahead of LinkParent for admission and
// display: a direct blocker is what's stopping num from proceeding right
// now, so it earns budget priority over an informational parent link.
func relationRank(r LinkRelation) int {
	if r == LinkBlockedBy {
		return 0
	}
	return 1
}

// renderLinkedIssues renders links -- num's transitive link chain from a
// LinkedIssueLister -- as the body of a "## Linked issues" section, fit
// within budget bytes (what's left of maxIssueTextBytes after the subject
// text).
//
// Entries are ordered shallowest depth first, blocked-by before parent
// within a depth, and admitted into that order greedily against budget: an
// entry too big to fit is never cut mid-body (a half-rendered issue is
// worse than none), it is omitted whole and listed by ref under "Omitted
// for size" instead, while a smaller lower-priority entry that still fits
// is still admitted. A resolution failure (Err set) is cheap to render as a
// one-line reason, so every one is listed under "Unresolved references"
// unconditionally rather than competing for admission at all.
//
// Sizing reserves for the heading, the (fully known) unresolved list, and
// the worst case where every resolvable entry lands in the omitted list,
// before spending any of what's left on full entry bodies -- so the
// admission loop below can never discover a fit that, once assembled,
// actually overruns budget. That reservation is deliberately conservative
// (a flat per-block separator charge rather than the exact N-1 count), so
// the assembled section can end a few bytes under budget but never over.
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

	// Reserve as if every resolvable entry ends up omitted -- the worst
	// case admission can produce -- before deciding which actually get
	// rendered in full.
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

// renderLinkedEntry renders one resolved LinkedIssue as a "### ref — title
// (relation of linkedFrom)" heading, a status line built only from what the
// adapter actually recorded (no derived or invented frontmatter), and the
// issue's own body verbatim -- which, for the local adapter, already
// carries its own "## Comments" section as plain body text.
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

// truncateIssueText caps s at maxIssueTextBytes, cutting on a rune boundary
// so the truncated tail cannot become a mangled half-rune, and appends an
// explicit, visible marker -- unlike a silent cut, a marker line makes the
// truncation itself part of what the prompt sees, rather than something an
// agent could mistake for the issue simply having no more text.
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
