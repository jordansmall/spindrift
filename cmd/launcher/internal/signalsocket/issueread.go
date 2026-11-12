package signalsocket

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/signalwire"
)

// IssueReader answers the issue read verb from the Dispatch's tracker. Which
// tracker and repository it reads is fixed when the Pass builds the reader,
// never taken from the request (ADR 0052): ref only names an issue within
// that scope, and the reader refuses one outside it. A returned Reject
// follows the package rule that Reason names no path, token or host detail.
type IssueReader interface {
	ReadIssue(ref string) (signalwire.IssueSnapshot, *signalwire.Reject)
}

func invalidRef() *signalwire.Reject {
	return &signalwire.Reject{
		Status: "invalid_ref",
		Reason: "ref must be exactly one non-empty utf-8 query value within the length limit",
		Code:   http.StatusBadRequest,
	}
}

// issue serves GET /issue?ref=<ref>. Like status it is a read, not an accept:
// nothing is buffered, so it takes the "read" disposition.
func (h *handler) issue(w http.ResponseWriter, r *http.Request) {
	rec := recordFrom(r)
	if r.Method != http.MethodGet {
		reject(w, rec, methodReject())
		return
	}
	// 501 rather than 404: the route exists, this Dispatch just has no
	// tracker to answer it from.
	if h.issues == nil {
		reject(w, rec, &signalwire.Reject{
			Status: "unsupported",
			Reason: "this dispatch has no issue reader",
			Code:   http.StatusNotImplemented,
		})
		return
	}
	refs := r.URL.Query()["ref"]
	if len(refs) != 1 || strings.TrimSpace(refs[0]) == "" || len(refs[0]) > signalwire.MaxIssueRefBytes || !utf8.ValidString(refs[0]) {
		reject(w, rec, invalidRef())
		return
	}
	snap, rej := h.issues.ReadIssue(refs[0])
	if rej != nil {
		reject(w, rec, rej)
		return
	}
	// The cap is enforced here, never trusted to the reader.
	reply, err := json.Marshal(signalwire.CapIssueSnapshot(snap))
	if err != nil {
		reject(w, rec, &signalwire.Reject{Status: "internal", Reason: "issue reply could not be encoded", Code: http.StatusInternalServerError})
		return
	}
	rec.decision = "read"
	rec.size = len(reply)
	rec.hash = contentHash(signalwire.Kind(signalwire.IssueRoute), string(reply))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(reply, '\n'))
}
