package signalwire

import (
	"slices"
	"unicode/utf8"
)

// IssueRoute is the read verb's path segment ("/issue"). It is not a buffered
// Kind: nothing is ever accepted under it, so it carries no Receipt. The server
// only borrows the Kind type to label the op-event content hash.
const IssueRoute = "issue"

// MaxIssueRefBytes bounds the ref query parameter of a read request.
const MaxIssueRefBytes = 256

// MaxIssueReplyBytes bounds the text of an IssueSnapshot (title, body, labels
// and comment fields). It is the same budget the host's ISSUE TEXT snapshot
// uses (internal/forge's maxIssueTextBytes), restated here because this
// package imports nothing outside the standard library. Worst-case JSON
// escaping can multiply it by six, which still fits signalclient's reply limit.
const MaxIssueReplyBytes = 64 * 1024

// IssueComment is one comment of an IssueSnapshot.
type IssueComment struct {
	Author    string `json:"author"`
	CreatedAt string `json:"createdAt"`
	Body      string `json:"body"`
}

// IssueSnapshot is the read verb's reply: the Dispatch's tracker issue as the
// host sees it. Comments run oldest first, so the tail is the most recent.
// Truncated reports that the snapshot is incomplete: the MaxIssueReplyBytes cap
// cut something, or the tracker could not list the comments.
type IssueSnapshot struct {
	Ref       string         `json:"ref"`
	Title     string         `json:"title"`
	State     string         `json:"state"` // "open" or "closed"
	Labels    []string       `json:"labels"`
	Body      string         `json:"body"`
	Comments  []IssueComment `json:"comments"`
	Truncated bool           `json:"truncated"`
}

// TextBytes is the size CapIssueSnapshot bounds: every string the snapshot
// carries.
func (s IssueSnapshot) TextBytes() int {
	n := len(s.Ref) + len(s.Title) + len(s.State) + len(s.Body)
	for _, l := range s.Labels {
		n += len(l)
	}
	for _, c := range s.Comments {
		n += len(c.Author) + len(c.CreatedAt) + len(c.Body)
	}
	return n
}

// CapIssueSnapshot returns s within MaxIssueReplyBytes of text, setting
// Truncated when it had to cut. The order is fixed so the reply is
// deterministic: oldest comments go first, then the body is cut on a rune
// boundary, then (only for a pathological reader) the title and labels.
func CapIssueSnapshot(s IssueSnapshot) IssueSnapshot {
	if s.TextBytes() <= MaxIssueReplyBytes {
		return s
	}
	s.Truncated = true
	s.Comments = slices.Clone(s.Comments)
	for len(s.Comments) > 0 && s.TextBytes() > MaxIssueReplyBytes {
		s.Comments = s.Comments[1:]
	}
	s.Body = cutUTF8(s.Body, len(s.Body)-(s.TextBytes()-MaxIssueReplyBytes))
	s.Title = cutUTF8(s.Title, len(s.Title)-(s.TextBytes()-MaxIssueReplyBytes))
	s.Labels = slices.Clone(s.Labels)
	for len(s.Labels) > 0 && s.TextBytes() > MaxIssueReplyBytes {
		s.Labels = s.Labels[:len(s.Labels)-1]
	}
	return s
}

// cutUTF8 returns the longest prefix of v within n bytes that ends on a rune
// boundary.
func cutUTF8(v string, n int) string {
	if n >= len(v) {
		return v
	}
	n = max(n, 0)
	for n > 0 && !utf8.RuneStart(v[n]) {
		n--
	}
	return v[:n]
}
