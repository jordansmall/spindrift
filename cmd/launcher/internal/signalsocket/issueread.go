package signalsocket

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
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

const (
	refQueryReason  = "ref must be exactly one non-empty utf-8 query value within the length limit"
	refSyntaxReason = "ref must be an issue number, #number, <scope>#number or KEY-number"
)

func invalidRef(reason string) *signalwire.Reject {
	return &signalwire.Reject{Status: "invalid_ref", Reason: reason, Code: http.StatusBadRequest}
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
		reject(w, rec, invalidRef(refQueryReason))
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

// slugRef and keyRef are the shapes a scope takes before a '#' ("owner/repo" or a
// Jira key); jiraRef is the KEY-number form.
var (
	slugRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9._-]+$`)
	keyRef  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
	jiraRef = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]*)-([0-9]+)$`)
)

func foreignRef() *signalwire.Reject {
	return &signalwire.Reject{
		Status: "foreign_ref",
		Reason: "ref names another repository or project; only this dispatch's tracker can be read",
		Code:   http.StatusForbidden,
	}
}

// canonicalNumber reports whether s is a positive decimal integer with no
// sign or leading zeros: the only number shape that ever reaches a tracker.
func canonicalNumber(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n >= 1 && strconv.Itoa(n) == s
}

type trackerIssueReader struct {
	t     forge.IssueTracker
	scope string
	warn  io.Writer
}

// NewTrackerIssueReader reads issues from t, which must already be scoped to
// the Dispatch's repository or Jira project. scope names that repository
// ("owner/repo") or project key; it comes from the Dispatch, never the
// request, and a ref naming any other scope is refused before t is called.
// t may be a native adapter or a Connector-backed one (connectortracker); on
// the latter the Connector's Route table gates the read exactly as it gates a
// host read. Failure detail goes to warn, never to the Box.
func NewTrackerIssueReader(t forge.IssueTracker, scope string, warn io.Writer) IssueReader {
	if warn == nil {
		warn = io.Discard
	}
	return trackerIssueReader{t: t, scope: scope, warn: warn}
}

// id resolves ref to the tracker's own issue id: a bare number for a
// repository scope, KEY-number for a Jira project scope (no '/').
func (r trackerIssueReader) id(ref string) (string, *signalwire.Reject) {
	num := ref
	switch {
	case strings.HasPrefix(ref, "#"):
		num = ref[1:]
	case strings.Contains(ref, "#"):
		prefix, rest, _ := strings.Cut(ref, "#")
		if !slugRef.MatchString(prefix) && !keyRef.MatchString(prefix) {
			return "", invalidRef(refSyntaxReason)
		}
		if !strings.EqualFold(prefix, r.scope) {
			return "", foreignRef()
		}
		num = rest
	case !canonicalNumber(ref):
		m := jiraRef.FindStringSubmatch(ref)
		if m == nil {
			return "", invalidRef(refSyntaxReason)
		}
		if !strings.EqualFold(m[1], r.scope) {
			return "", foreignRef()
		}
		num = m[2]
	}
	// Every arm ends here, so a number is checked once however it was spelled.
	if !canonicalNumber(num) {
		return "", invalidRef(refSyntaxReason)
	}
	// Rebuilt from the parsed integer, so no byte of the Box-sent ref reaches
	// a tracker that splices ids into a file path or URL.
	n, _ := strconv.Atoi(num)
	if strings.Contains(r.scope, "/") {
		return strconv.Itoa(n), nil
	}
	return r.scope + "-" + strconv.Itoa(n), nil
}

func (r trackerIssueReader) ReadIssue(ref string) (signalwire.IssueSnapshot, *signalwire.Reject) {
	id, rej := r.id(ref)
	if rej != nil {
		return signalwire.IssueSnapshot{}, rej
	}
	th, err := forge.IssueThread(r.t, id, r.warn)
	if err != nil {
		fmt.Fprintf(r.warn, "    ?? signal issue read %s: %v\n", id, err)
		switch {
		case errors.Is(err, connectorhost.ErrRouteRefused):
			return signalwire.IssueSnapshot{}, &signalwire.Reject{Status: "refused", Reason: "the tracker's route table refused this read", Code: http.StatusForbidden}
		case errors.Is(err, forge.ErrNotFound):
			return signalwire.IssueSnapshot{}, &signalwire.Reject{Status: "not_found", Reason: "no such issue in this dispatch's tracker", Code: http.StatusNotFound}
		}
		return signalwire.IssueSnapshot{}, &signalwire.Reject{Status: "tracker_error", Reason: "the tracker read failed", Code: http.StatusBadGateway}
	}
	// A tracker can resolve a bare id to another project's issue; the
	// answer must be the issue asked for or nothing.
	if !strings.EqualFold(th.Issue.Number, id) {
		return signalwire.IssueSnapshot{}, foreignRef()
	}
	state := "open"
	if th.Issue.State == forge.IssueClosed {
		state = "closed"
	}
	// Truncated is the one "this thread is incomplete" signal the wire has, so an
	// unreadable comment fetch sets it rather than reading as "no comments".
	snap := signalwire.IssueSnapshot{Ref: id, Title: th.Issue.Title, State: state, Labels: th.Issue.Labels, Body: th.Issue.Body, Truncated: th.CommentsErr != nil}
	for _, c := range th.Comments {
		snap.Comments = append(snap.Comments, signalwire.IssueComment{Author: c.Author, CreatedAt: c.CreatedAt, Body: c.Body})
	}
	return snap, nil
}
