package forge

import "strings"

// Snapshot returns the frozen issue-read text for num against tracker: if
// tracker implements SnapshotReader, its Snapshot(num) result verbatim —
// including any error, which is returned as-is rather than masked by a
// fallback; otherwise tracker.Issue(num).Body (the local degrade — its
// comments are already inline in the body, so there is nothing a separate
// Snapshot call would add), plus trailing "parent: <value>", "state:
// <value>", and "labels: <comma-separated>" lines, each appended only when
// the corresponding Issue field is non-empty/non-zero (skipped entirely
// otherwise), in that order. Issue(num).Body is the local adapter's
// Markdown body alone, stripped of its YAML frontmatter (ADR 0013) —
// Parent, State, and Labels are all frontmatter-derived, so without this
// they would vanish from the snapshot entirely: a local issue-read
// fragment's "follow its parent link" instruction would be unfollowable,
// and the issue's state/labels would silently disappear from what a local
// box's issue-read produces (issue #2547).
func Snapshot(tracker IssueTracker, num string) (string, error) {
	if sr, ok := tracker.(SnapshotReader); ok {
		return sr.Snapshot(num)
	}
	iss, err := tracker.Issue(num)
	if err != nil {
		return "", err
	}
	text := iss.Body
	if iss.Parent != "" {
		text += "\n\nparent: " + iss.Parent
	}
	if iss.State != "" {
		text += "\n\nstate: " + string(iss.State)
	}
	if len(iss.Labels) > 0 {
		text += "\n\nlabels: " + strings.Join(iss.Labels, ", ")
	}
	return text, nil
}

// CommentAttribution is one comment's author, timestamp, and body — the
// shape FormatSnapshot renders into a SnapshotReader implementation's
// frozen issue-read text.
type CommentAttribution struct {
	Author    string
	CreatedAt string
	Body      string
}

// FormatSnapshot renders body plus the last 10 of comments into the frozen
// issue-read text every SnapshotReader implementation returns: the issue
// body, a blank line, then one "<author> (<createdAt>): <body>" line per
// kept comment, in original chronological order. Comments beyond the last
// 10 are dropped from the front, mirroring `gh issue view --json comments
// --jq '.comments[-10:]'`. Zero comments renders as just the body, with no
// dangling trailing blank line — shared by the github and forgejo
// SnapshotReader implementations so this formatting exists exactly once.
func FormatSnapshot(body string, comments []CommentAttribution) string {
	if len(comments) > 10 {
		comments = comments[len(comments)-10:]
	}
	if len(comments) == 0 {
		return body
	}
	var b strings.Builder
	b.WriteString(body)
	b.WriteString("\n\n")
	for i, c := range comments {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(c.Author)
		b.WriteString(" (")
		b.WriteString(c.CreatedAt)
		b.WriteString("): ")
		b.WriteString(c.Body)
	}
	return b.String()
}
