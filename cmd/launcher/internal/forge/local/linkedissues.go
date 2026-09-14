package local

import (
	"fmt"

	"spindrift.dev/launcher/internal/forge"
)

var _ forge.LinkedIssueLister = (*LocalTracker)(nil)

// linkedIssueQueueEntry is one pending node in LinkedIssues' breadth-first
// walk: the issue's number and already-parsed contents (carried through so
// a node resolved once during ref resolution is never read a second time
// when it is dequeued), and the depth it was reached at.
type linkedIssueQueueEntry struct {
	num   string
	li    localIssue
	depth int
}

// LinkedIssues implements forge.LinkedIssueLister: a breadth-first walk of
// num's "## Blocked by" and parent: references, each linked issue visited
// once at its shallowest depth. num itself seeds the visited set, so a
// blocked-by or parent back-link to the subject terminates instead of
// re-emitting it.
//
// Per-reference resolution failures (a missing file, a malformed file, a
// parent: value that isn't a local slug) become entries with Err set rather
// than aborting the walk -- only num's own read failure is returned as an
// error, since without num there is nothing to walk.
func (lt *LocalTracker) LinkedIssues(num string) ([]forge.LinkedIssue, error) {
	subjectLi, err := lt.readIssueFile(num)
	if err != nil {
		return nil, err
	}

	visited := map[string]bool{num: true}
	queue := []linkedIssueQueueEntry{{num: num, li: subjectLi, depth: 0}}
	var links []forge.LinkedIssue

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		li := cur.li
		depth := cur.depth + 1

		for _, ref := range parseLocalBlockers(li.body) {
			if visited[ref] {
				continue
			}
			visited[ref] = true
			entry := forge.LinkedIssue{
				Ref:        ref,
				Relation:   forge.LinkBlockedBy,
				LinkedFrom: cur.num,
				Depth:      depth,
			}
			if refLi, err := lt.readIssueFile(ref); err != nil {
				entry.Err = err
			} else {
				iss := toIssue(ref, refLi)
				entry.Issue = &iss
				queue = append(queue, linkedIssueQueueEntry{num: ref, li: refLi, depth: depth})
			}
			links = append(links, entry)
		}

		if parent := li.frontmatter.Parent; parent != "" && !visited[parent] {
			visited[parent] = true
			entry := forge.LinkedIssue{
				Ref:        parent,
				Relation:   forge.LinkParent,
				LinkedFrom: cur.num,
				Depth:      depth,
			}
			if _, pathErr := lt.slugPath(parent); pathErr != nil {
				entry.Err = fmt.Errorf("parent %q is not a local issue slug", parent)
			} else if refLi, readErr := lt.readIssueFile(parent); readErr != nil {
				entry.Err = readErr
			} else {
				iss := toIssue(parent, refLi)
				entry.Issue = &iss
				queue = append(queue, linkedIssueQueueEntry{num: parent, li: refLi, depth: depth})
			}
			links = append(links, entry)
		}
	}

	return links, nil
}
