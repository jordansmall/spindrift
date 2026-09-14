package forge

// LinkRelation names how a linked issue was reached from the issue that
// referenced it.
type LinkRelation string

const (
	// LinkBlockedBy is a "## Blocked by" body reference.
	LinkBlockedBy LinkRelation = "blocked-by"
	// LinkParent is an issue's parent: frontmatter reference (ADR 0033).
	LinkParent LinkRelation = "parent"
)

// LinkedIssue is one entry of an issue's link chain, as returned by
// LinkedIssueLister.LinkedIssues. Exactly one of Issue and Err is set: Issue
// when Ref resolved to a real local issue file that parsed cleanly, Err
// otherwise -- a missing file, a malformed file, or a reference (a URL, a
// Jira key) that was never a local slug to begin with.
type LinkedIssue struct {
	// Ref is the raw reference as written -- a blocked-by slug, or a
	// parent: value, unmodified.
	Ref string
	// Relation says whether Ref came from a "## Blocked by" bullet or a
	// parent: field.
	Relation LinkRelation
	// LinkedFrom is the issue number whose body or frontmatter carried Ref.
	LinkedFrom string
	// Depth is 1 for a link taken directly off the subject issue, 2 for a
	// link taken off one of those, and so on.
	Depth int
	// Issue is the resolved, parsed issue Ref points at -- nil when Ref
	// could not be resolved (see Err).
	Issue *Issue
	// Err explains why Ref did not yield an Issue -- nil when Issue is set.
	Err error
}

// LinkedIssueLister is the optional IssueTracker surface for adapters that
// can resolve an issue's whole link chain host-side, rather than leaving a
// Box to re-derive it by re-reading raw body text one hop at a time.
// Discovered via type assertion, like CommentLister and the other optional
// IssueTracker surfaces: a remote tracker (github, jira) has no local
// notion of a "## Blocked by" section or a parent: frontmatter field to
// walk, so it deliberately doesn't implement this -- only the local
// file-backed adapter, whose issues reference each other by slug, has a
// chain to resolve.
type LinkedIssueLister interface {
	// LinkedIssues returns issue num's transitive link chain, breadth
	// first: each linked issue appears at most once, at the shallowest
	// depth it is reachable from num, and a cycle back to num itself or to
	// any already-visited issue terminates that branch rather than
	// recursing forever. A per-reference resolution failure (a missing
	// file, a malformed file, a parent: value that isn't a local slug)
	// degrades to an entry with Err set, never a fatal error -- only a
	// failure to read num itself is returned as an error, since without
	// num there is no chain to walk at all.
	LinkedIssues(num string) ([]LinkedIssue, error)
}
