package signalwire

import "slices"

// IssueSearchRoute is the search verb's path segment ("/issue-search"). Like
// IssueRoute it is not a buffered Kind; the server only borrows the type to
// label the op-event content hash.
const IssueSearchRoute = "issue-search"

const (
	// MaxIssueSearchTextBytes bounds the text query parameter.
	MaxIssueSearchTextBytes = 256
	// MaxIssueSearchResults bounds the results of one search.
	MaxIssueSearchResults = 20
	// MaxIssueSearchTitleBytes bounds each result's title.
	MaxIssueSearchTitleBytes = 512
)

// issueSearchLabels is the fixed set the label filter may name: the three
// host-filed finding provenance labels plus the research reject verdict label,
// which is all a dedup search needs. A Box can never aim the search at a
// dispatch or triage label.
var issueSearchLabels = []string{
	"agent-review-finding",
	"agent-research-reject",
	"agent-research-finding",
	"agent-butler-finding",
}

// IssueSearchLabel reports whether l is in the fixed set the search's label
// filter accepts.
func IssueSearchLabel(l string) bool { return slices.Contains(issueSearchLabels, l) }

// IssueSearchResult is one hit of an IssueSearchReply.
type IssueSearchResult struct {
	Number string   `json:"number"`
	Title  string   `json:"title"`
	State  string   `json:"state"` // "open" or "closed"
	Labels []string `json:"labels"`
}

// IssueSearchReply is the search verb's reply. Truncated reports that the cap
// cut a result or a title.
type IssueSearchReply struct {
	Issues    []IssueSearchResult `json:"issues"`
	Truncated bool                `json:"truncated"`
}

// TextBytes is the size CapIssueSearch bounds: every string the reply carries.
func (r IssueSearchReply) TextBytes() int {
	n := 0
	for _, i := range r.Issues {
		n += len(i.Number) + len(i.Title) + len(i.State)
		for _, l := range i.Labels {
			n += len(l)
		}
	}
	return n
}

// CapIssueSearch returns r within MaxIssueSearchResults results, each title
// within MaxIssueSearchTitleBytes, and the whole within MaxIssueReplyBytes of
// text, setting Truncated when it had to cut. Excess results go from the tail,
// so the hits first in the tracker's own order survive. Issues and each
// Labels encode as [], never null.
func CapIssueSearch(r IssueSearchReply) IssueSearchReply {
	if len(r.Issues) > MaxIssueSearchResults {
		r.Truncated = true
	}
	r.Issues = slices.Clone(r.Issues[:min(len(r.Issues), MaxIssueSearchResults)])
	if r.Issues == nil {
		r.Issues = []IssueSearchResult{}
	}
	for i := range r.Issues {
		if len(r.Issues[i].Title) > MaxIssueSearchTitleBytes {
			r.Issues[i].Title = cutUTF8(r.Issues[i].Title, MaxIssueSearchTitleBytes)
			r.Truncated = true
		}
		if r.Issues[i].Labels == nil {
			r.Issues[i].Labels = []string{}
		}
	}
	for len(r.Issues) > 0 && r.TextBytes() > MaxIssueReplyBytes {
		r.Issues = r.Issues[:len(r.Issues)-1]
		r.Truncated = true
	}
	return r
}
