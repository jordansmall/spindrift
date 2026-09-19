package local

import (
	"fmt"

	"spindrift.dev/launcher/internal/forge"
)

var _ forge.LinkedIssueLister = (*LocalTracker)(nil)

// linkedIssueQueueEntry carries each node's already-parsed contents so the walk
// does not re-read an issue when it dequeues that node.
type linkedIssueQueueEntry struct {
	num   string
	li    localIssue
	depth int
}

// LinkedIssues walks num's "## Blocked by" and parent: references
// breadth-first, visiting each linked issue once at its shallowest depth. num
// seeds the visited set, so a back-link to the subject terminates instead of
// re-emitting it. A reference that fails to resolve becomes an entry with Err
// set; only num's own read failure aborts the walk.
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
