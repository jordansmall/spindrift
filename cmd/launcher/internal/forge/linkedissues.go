package forge

// LinkRelation names how a linked issue was reached from the issue referencing it.
type LinkRelation string

const (
	// LinkBlockedBy is a "## Blocked by" body reference.
	LinkBlockedBy LinkRelation = "blocked-by"
	// LinkParent is an issue's parent: frontmatter reference (ADR 0033).
	LinkParent LinkRelation = "parent"
)

// LinkedIssue is one entry of an issue's link chain. Exactly one of Issue and
// Err is set.
type LinkedIssue struct {
	// Ref is the raw reference as written, unmodified.
	Ref      string
	Relation LinkRelation
	// LinkedFrom is the issue number whose body or frontmatter carried Ref.
	LinkedFrom string
	// Depth is 1 for a link taken directly off the subject issue, 2 for a
	// link taken off one of those, and so on.
	Depth int
	Issue *Issue
	// Err explains why Ref did not resolve: a missing file, a malformed file,
	// or a reference (a URL, a Jira key) that was never a local slug.
	Err error
}

// LinkedIssueLister resolves an issue's whole link chain host-side, so a Box
// need not re-derive it from raw body text one hop at a time. Adapters are
// discovered by type assertion. Only the local file-backed adapter implements
// it: a remote tracker has no "## Blocked by" section or parent: field to walk.
type LinkedIssueLister interface {
	// LinkedIssues returns issue num's transitive link chain, breadth first:
	// each linked issue appears once, at the shallowest depth it is reachable
	// from num, and a cycle back to an already-visited issue terminates that
	// branch. A per-reference resolution failure degrades to an entry with Err
	// set; only a failure to read num itself is returned as an error.
	LinkedIssues(num string) ([]LinkedIssue, error)
}
