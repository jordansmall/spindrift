package signalsocket

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/signalwire"
)

// IssueSearcher answers the issue-search verb from the Dispatch's tracker,
// scoped like IssueReader. The handler has already validated the query.
type IssueSearcher interface {
	// SearchIssues returns at most limit hits. The handler asks for one more
	// than it will return, so truncation is detectable, and caps the reply
	// itself.
	SearchIssues(text string, state forge.IssueState, label string, limit int) (signalwire.IssueSearchReply, *signalwire.Reject)
}

type trackerIssueSearcher struct {
	s    forge.IssueSearcher
	warn io.Writer
}

// NewTrackerIssueSearcher searches through s, already scoped to the Dispatch's
// repository. A Connector serving the search gates it by its Route table; one
// not advertising tracker.searchIssues falls through to the native adapter
// it wraps, if any, scoped but ungated. Failure detail goes to warn, never to
// the Box.
func NewTrackerIssueSearcher(s forge.IssueSearcher, warn io.Writer) IssueSearcher {
	if warn == nil {
		warn = io.Discard
	}
	return trackerIssueSearcher{s: s, warn: warn}
}

func (t trackerIssueSearcher) SearchIssues(text string, state forge.IssueState, label string, limit int) (signalwire.IssueSearchReply, *signalwire.Reject) {
	found, err := t.s.SearchIssues(text, state, label, limit)
	if err != nil {
		fmt.Fprintf(t.warn, "    ?? signal issue search: %v\n", err)
		return signalwire.IssueSearchReply{}, trackerReject(err)
	}
	var reply signalwire.IssueSearchReply
	for _, iss := range found {
		reply.Issues = append(reply.Issues, signalwire.IssueSearchResult{Number: iss.Number, Title: iss.Title, State: iss.State.Wire(), Labels: iss.Labels})
	}
	return reply, nil
}

func invalidQuery(reason string) *signalwire.Reject {
	return &signalwire.Reject{Status: "invalid_query", Reason: reason, Code: http.StatusBadRequest}
}

// searchQuery validates the request's query. Only text, state and label are
// accepted: any other key is refused, so the scope can never be widened by the
// request.
func searchQuery(q url.Values) (text string, state forge.IssueState, label string, rej *signalwire.Reject) {
	for k := range q {
		if k != "text" && k != "state" && k != "label" {
			return "", "", "", invalidQuery("only the text, state and label query parameters are accepted")
		}
	}
	ts := q["text"]
	if len(ts) != 1 || strings.TrimSpace(ts[0]) == "" || len(ts[0]) > signalwire.MaxIssueSearchTextBytes || !utf8.ValidString(ts[0]) {
		return "", "", "", invalidQuery("text must be exactly one non-empty utf-8 query value within the length limit")
	}
	switch ss := q["state"]; {
	case len(ss) == 1 && ss[0] == "open":
		state = forge.IssueOpen
	case len(ss) == 1 && ss[0] == "closed":
		state = forge.IssueClosed
	default:
		return "", "", "", invalidQuery("state must be exactly one of open or closed")
	}
	if ls, ok := q["label"]; ok {
		if len(ls) != 1 || !signalwire.IssueSearchLabel(ls[0]) {
			return "", "", "", invalidQuery("label must be exactly one of the finding labels")
		}
		label = ls[0]
	}
	return ts[0], state, label, nil
}

// issueSearch serves GET /issue-search?text=<t>&state=open|closed[&label=<l>].
func (h *handler) issueSearch(w http.ResponseWriter, r *http.Request) {
	rec := recordFrom(r)
	if r.Method != http.MethodGet {
		reject(w, rec, methodReject())
		return
	}
	if h.search == nil {
		reject(w, rec, &signalwire.Reject{
			Status: "unsupported",
			Reason: "this dispatch has no issue search",
			Code:   http.StatusNotImplemented,
		})
		return
	}
	text, state, label, rej := searchQuery(r.URL.Query())
	if rej != nil {
		reject(w, rec, rej)
		return
	}
	found, rej := h.search.SearchIssues(text, state, label, signalwire.MaxIssueSearchResults+1)
	if rej != nil {
		reject(w, rec, rej)
		return
	}
	// The cap is enforced here, never trusted to the searcher.
	reply, err := json.Marshal(signalwire.CapIssueSearch(found))
	if err != nil {
		reject(w, rec, &signalwire.Reject{Status: "internal", Reason: "issue search reply could not be encoded", Code: http.StatusInternalServerError})
		return
	}
	rec.decision = "read"
	rec.size = len(reply)
	rec.hash = contentHash(signalwire.Kind(signalwire.IssueSearchRoute), string(reply))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(reply, '\n'))
}
