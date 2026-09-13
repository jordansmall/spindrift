package forge

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// maxIssueTextBytes bounds the string IssueText returns. An issue thread
// with an unbounded comment count has no length limit of its own, but the
// value travels into a Box as a single `-e ISSUE_TEXT=value` argument
// (dispatch.buildBoxEnv), which does -- so IssueText truncates rather than
// letting a pathological thread blow past that per-arg limit.
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
// A t.Issue error is returned -- there is no text to build without it. A
// comments-fetch error is deliberately swallowed: it degrades to the
// body-only text rather than failing IssueText, since losing the comment
// snapshot must never fail a dispatch that would otherwise have proceeded
// on the body alone.
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
	return truncateIssueText(text), nil
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
