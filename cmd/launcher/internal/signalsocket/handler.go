package signalsocket

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/signalwire"
)

// statusKind tags a GET /status op in the mirror. It is not a Kind: nothing is
// ever buffered under it.
const statusKind = "status"

// unknownKind tags a request outside the known routes, and a bare "/usage".
// It is a fixed token, never the Box-controlled path, so a caller cannot
// write its own text into the launcher's log.
const unknownKind = "unknown"

type handler struct {
	buf *Buffer

	// issues answers the read verb; nil when the Dispatch has no tracker to
	// read from.
	issues IssueReader

	// search answers the search verb; nil when the tracker cannot be read or
	// cannot search. It is already scoped to the Dispatch's repository, so the
	// request names no scope.
	search IssueSearcher

	// logMu serialises the mirror's writes, so concurrent requests cannot
	// splice one stream-json line into another.
	logMu sync.Mutex
	logw  io.Writer
}

type opRecordKey struct{}

// opRecord accumulates one request's mirror fields; the mirror middleware
// emits it as the request's one hostop.SpindriftOp.
type opRecord struct {
	kind     string
	usage    bool
	size     int
	hash     string
	decision string
	reason   string
}

// recordFrom returns the current request's opRecord, always non-nil inside a
// route handler since the mirror runs first.
func recordFrom(r *http.Request) *opRecord {
	rec, _ := r.Context().Value(opRecordKey{}).(*opRecord)
	return rec
}

// Handler is the signal socket's http.Handler. gated travels with the chain
// because only the assembly point knows whether a TCP secret gate was
// installed, and ListenAndServeTCP refuses anything ungated.
type Handler struct {
	http.Handler
	gated bool
}

// routes dispatches on the kind kindForPath derived, not an http.ServeMux: a
// ServeMux answers a path it cleans (doubled slash, dot segment) with its own
// 307 before any route arm can set a decision. This keeps "kind is the route"
// exact-match-or-nothing.
func (h *handler) routes(w http.ResponseWriter, r *http.Request) {
	rec := recordFrom(r)
	if rec.usage {
		usageReport(w, r)
		return
	}
	switch rec.kind {
	case string(signalwire.KindComment):
		serve(w, r, h.buf.AcceptComment)
	case string(signalwire.KindPRIntent):
		serve(w, r, h.buf.AcceptPRIntent)
	case string(signalwire.KindIssueIntent):
		serve(w, r, h.buf.AcceptIssueIntent)
	case signalwire.IssueRoute:
		h.issue(w, r)
	case signalwire.IssueSearchRoute:
		h.issueSearch(w, r)
	case statusKind:
		h.status(w, r)
	default:
		unknownRoute(w, r)
	}
}

// NewHandler returns the signal socket's ungated Handler (ADR 0052), mirroring
// one spindrift_op event per request to logw (issue #3724); a nil logw
// discards the mirror, and a nil issues or search makes its route reject. Only
// the unix transport uses it: the socket file's permissions are its gate.
func NewHandler(b *Buffer, logw io.Writer, issues IssueReader, search IssueSearcher) *Handler {
	h := &handler{buf: b, logw: logw, issues: issues, search: search}
	return &Handler{Handler: h.mirror(http.HandlerFunc(h.routes))}
}

// NewGatedHandler returns a Handler for the every-interface TCP fallback:
// every request must present secret via signalwire.SecretHeader. An empty
// secret would match an absent header, so it fails closed. The gate sits
// inside the mirror, so a rejected request still emits its event.
func NewGatedHandler(b *Buffer, logw io.Writer, secret string, issues IssueReader, search IssueSearcher) (*Handler, error) {
	if secret == "" {
		return nil, errors.New("signalsocket: refusing to gate a handler with an empty secret")
	}
	h := &handler{buf: b, logw: logw, issues: issues, search: search}
	return &Handler{Handler: h.mirror(gate(secret, http.HandlerFunc(h.routes))), gated: true}, nil
}

// gate wraps next behind a constant-time comparison of signalwire.SecretHeader
// against secret. It runs inside the mirror, so a wrong secret refuses through
// reject rather than a bare http.Error the mirror never sees.
func gate(secret string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Constant-time: this header is the sole gate on a reachable port, and
		// == would leak the secret a byte at a time. The early return on
		// differing lengths leaks only len(secret), not a comparable oracle.
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(signalwire.SecretHeader)), []byte(secret)) != 1 {
			// The body is never read: an unauthenticated caller pushes no
			// bytes through, so the event's size and hash stay zero.
			reject(w, recordFrom(r), &signalwire.Reject{
				Status: "unauthorized",
				Reason: "missing or wrong tcp secret",
				Code:   http.StatusUnauthorized,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// kindForPath derives a request's mirror kind from its path alone, so every
// request gets one before routing. Bare "/usage" is checked first: stripping
// its suffix would leave "".
func kindForPath(path string) (kind string, isUsage bool) {
	if path == signalwire.UsagePath("") {
		return unknownKind, true
	}
	if rest, ok := strings.CutSuffix(path, "/"+signalwire.UsageRoute); ok {
		path, isUsage = rest, true
	}
	switch path {
	case "/comment":
		return string(signalwire.KindComment), isUsage
	case "/pr-intent":
		return string(signalwire.KindPRIntent), isUsage
	case "/issue-intent":
		return string(signalwire.KindIssueIntent), isUsage
	case "/" + signalwire.IssueRoute:
		return signalwire.IssueRoute, isUsage
	case "/" + signalwire.IssueSearchRoute:
		return signalwire.IssueSearchRoute, isUsage
	case "/status":
		return statusKind, isUsage
	default:
		// A usage-suffixed path with an unknown prefix is unknown outright,
		// never a usage report for a kind that isn't real.
		return unknownKind, false
	}
}

// mirror wraps next so "exactly one event per request" is structural rather
// than a convention each route arm must remember.
func (h *handler) mirror(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// decision starts "reject", not "": FormatSpindriftOp renders an
		// unrecognised decision, "" included, through its accept arm, so a
		// record no route arm touches must never render as one.
		kind, usage := kindForPath(r.URL.Path)
		rec := &opRecord{kind: kind, usage: usage, decision: "reject"}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), opRecordKey{}, rec)))
		h.emit(hostop.SpindriftOp{
			Op:       hostop.OpSignal,
			Kind:     rec.kind,
			Decision: rec.decision,
			Reason:   rec.reason,
			Size:     rec.size,
			Hash:     rec.hash,
		})
	})
}

func (h *handler) emit(op hostop.SpindriftOp) {
	if h.logw == nil {
		return
	}
	line := hostop.EncodeSpindriftOp(op)
	if line == "" {
		return
	}
	h.logMu.Lock()
	defer h.logMu.Unlock()
	_, _ = io.WriteString(h.logw, line)
}

// serve runs one POST route. Content is the request type itself, with no
// per-route wrapper, so a destination field is unrepresentable.
func serve[T any](w http.ResponseWriter, r *http.Request, accept func(T) (signalwire.Receipt, *signalwire.Reject)) {
	rec := recordFrom(r)
	if r.Method != http.MethodPost {
		reject(w, rec, methodReject())
		return
	}
	var content T
	if rej := decode(w, r, &content); rej != nil {
		reject(w, rec, rej)
		return
	}
	rcpt, rej := accept(content)
	// The receipt is computed whether the buffer accepts or refuses the
	// content, so size and hash are known either way.
	rec.size, rec.hash = rcpt.Bytes, rcpt.Hash
	if rej != nil {
		reject(w, rec, rej)
		return
	}
	rec.decision = "accept"
	writeJSON(w, http.StatusOK, rcpt)
}

func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	rec := recordFrom(r)
	if r.Method != http.MethodGet {
		reject(w, rec, methodReject())
		return
	}
	// The buffer never mediated a GET, so it is not an "accept".
	rec.decision = "read"
	writeJSON(w, http.StatusOK, h.buf.Status())
}

// usageReport answers the diagnostics-only usage route (ADR 0052): a
// client-side usage failure (unknown flag, missing required flag) that never
// reached a real route. It never touches the buffer and never changes the
// run's outcome.
func usageReport(w http.ResponseWriter, r *http.Request) {
	rec := recordFrom(r)
	if r.Method != http.MethodPost {
		reject(w, rec, methodReject())
		return
	}
	var report signalwire.UsageReport
	if rej := decode(w, r, &report); rej != nil {
		reject(w, rec, rej)
		return
	}
	if report.Reason == "" {
		reject(w, rec, malformed())
		return
	}
	if len(report.Reason) > signalwire.MaxUsageReasonBytes {
		reject(w, rec, &signalwire.Reject{
			Status: "oversize",
			Reason: fmt.Sprintf("reason exceeds the %d-byte limit", signalwire.MaxUsageReasonBytes),
			Code:   http.StatusRequestEntityTooLarge,
		})
		return
	}
	rec.decision = "usage"
	rec.reason = report.Reason
	w.WriteHeader(http.StatusNoContent)
}

// unknownRoute answers any path outside the known routes; size and hash stay
// zero.
func unknownRoute(w http.ResponseWriter, r *http.Request) {
	reject(w, recordFrom(r), &signalwire.Reject{
		Status: "unknown_kind",
		Reason: "no such signal kind",
		Code:   http.StatusNotFound,
	})
}

func methodReject() *signalwire.Reject {
	return &signalwire.Reject{
		Status: "method_not_allowed",
		Reason: "this signal kind does not answer that method",
		Code:   http.StatusMethodNotAllowed,
	}
}

func decode(w http.ResponseWriter, r *http.Request, content any) *signalwire.Reject {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, signalwire.MaxRequestBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return &signalwire.Reject{
				Status: "oversize",
				Reason: fmt.Sprintf("request exceeds the %d-byte limit", signalwire.MaxRequestBytes),
				Code:   http.StatusRequestEntityTooLarge,
			}
		}
		return malformed()
	}
	// The raw bytes are the only place invalid utf-8 is still visible:
	// encoding/json substitutes U+FFFD for it inside a string, so a check
	// behind the decoder would hash and accept bytes the Box never sent.
	if !utf8.Valid(raw) {
		return signalwire.InvalidUTF8Reject("request body")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(content); err != nil {
		return malformed()
	}
	// One object per request: anything trailing it is a second signal the
	// receipt would not cover.
	if dec.More() {
		return malformed()
	}
	return nil
}

func malformed() *signalwire.Reject {
	return &signalwire.Reject{
		Status: "malformed_json",
		Reason: "body is not a json object with exactly this signal kind's fields",
		Code:   http.StatusBadRequest,
	}
}

// reject records rej onto rec for the mirror and writes it as the reply.
func reject(w http.ResponseWriter, rec *opRecord, rej *signalwire.Reject) {
	rec.decision = "reject"
	rec.reason = rej.Reason
	writeJSON(w, rej.Code, rej)
}

func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	// The status line is already committed by the time an encode could
	// fail, so a failure has nowhere to go but the client's short read.
	_ = json.NewEncoder(w).Encode(payload)
}
