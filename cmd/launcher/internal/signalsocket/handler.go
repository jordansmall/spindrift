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
	"sync"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/signalwire"
)

// statusKind tags a GET /status op in the mirror. It is not a Kind: nothing is
// ever buffered under it.
const statusKind = "status"

// unknownKind tags a request outside the four routes. It is a fixed token,
// never the requested path -- the path is Box-controlled and the mirror is a
// log line, so echoing the path back would let a caller write its own text
// into the launcher's log.
const unknownKind = "unknown"

type handler struct {
	buf *Buffer

	// logMu serialises the mirror's writes: requests are served
	// concurrently, and two interleaved writes would splice one stream-json
	// line into another.
	logMu sync.Mutex
	logw  io.Writer
}

// opRecordKey is the context key that carries the per-request opRecord.
// Unexported so only this package can stash or read one.
type opRecordKey struct{}

// opRecord accumulates one request's mirror fields as it moves through
// routing, decoding and accept. The mirror middleware creates it, stashes it
// on the request context, and emits it as the request's one claude.SpindriftOp
// once the inner handler returns.
type opRecord struct {
	kind     string
	size     int
	hash     string
	decision string
	reason   string
}

// recordFrom returns the current request's opRecord. Every request the mux
// serves passes through the mirror middleware first, so this is always
// non-nil inside a route handler.
func recordFrom(r *http.Request) *opRecord {
	rec, _ := r.Context().Value(opRecordKey{}).(*opRecord)
	return rec
}

// Handler is the signal socket's http.Handler. It is a named type rather than
// a bare http.Handler so that gated travels with the chain: only the assembly
// point knows whether a TCP secret gate was installed inside the mirror, and
// ListenAndServeTCP has to refuse anything else.
type Handler struct {
	http.Handler
	gated bool
}

// routes dispatches on recordFrom(r).kind, the same kind kindForPath already
// derived for the mirror ahead of routing, rather than registering paths on
// an http.ServeMux. A ServeMux answers a request whose path it cleans (a
// doubled slash, a dot segment) with its own 307 redirect before any
// registered handler runs, so no route arm ever gets a chance to set a
// decision on it; dispatching on the already-derived kind instead makes
// "kind is the route" exact-match-or-nothing, with no cleaning step in
// between.
func (h *handler) routes(w http.ResponseWriter, r *http.Request) {
	switch recordFrom(r).kind {
	case string(signalwire.KindComment):
		serve(w, r, h.buf.AcceptComment)
	case string(signalwire.KindPRIntent):
		serve(w, r, h.buf.AcceptPRIntent)
	case string(signalwire.KindIssueIntent):
		serve(w, r, h.buf.AcceptIssueIntent)
	case statusKind:
		h.status(w, r)
	default:
		unknownRoute(w, r)
	}
}

// NewHandler returns the signal socket's Handler, serving b's four routes
// and mirroring one spindrift_op event per request to logw (ADR 0052, issue
// #3724). A nil logw discards the mirror. The unix transport is the only
// caller: it has the socket file's permissions as its own gate, so this
// Handler carries none.
func NewHandler(b *Buffer, logw io.Writer) *Handler {
	h := &handler{buf: b, logw: logw}
	return &Handler{Handler: h.mirror(http.HandlerFunc(h.routes))}
}

// NewGatedHandler returns a Handler for the loopback TCP fallback, which has
// no filesystem permissions of its own: every request must present secret
// via signalwire.SecretHeader before it reaches a route. An empty secret
// would match an absent header, so it fails closed here rather than at
// listen time. The gate sits inside the mirror (gate wraps routes, mirror
// wraps gate), so a rejected request still emits its one spindrift_op event.
func NewGatedHandler(b *Buffer, logw io.Writer, secret string) (*Handler, error) {
	if secret == "" {
		return nil, errors.New("signalsocket: refusing to gate a handler with an empty secret")
	}
	h := &handler{buf: b, logw: logw}
	return &Handler{Handler: h.mirror(gate(secret, http.HandlerFunc(h.routes))), gated: true}, nil
}

// gate wraps next behind a constant-time comparison of signalwire.SecretHeader
// against secret. It runs inside the mirror middleware (recordFrom(r) already
// has an opRecord to write into), so a wrong-secret request rejects through
// the same reject helper every other refusal uses instead of a bare
// http.Error that the mirror never sees.
func gate(secret string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// subtle.ConstantTimeCompare, not ==: this header is the sole gate on
		// a port any local process can reach, so a short-circuiting == would
		// leak secret a byte at a time. The early return on differing lengths
		// leaks only len(secret), which is not a comparable oracle.
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(signalwire.SecretHeader)), []byte(secret)) != 1 {
			// The body is never read here, on purpose: an unauthenticated
			// caller must not get to push bytes through the socket at all,
			// so the mirrored event's size and hash stay zero rather than
			// describing content the gate had no business looking at.
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

// kindForPath derives a request's mirror kind from its path alone, ahead of
// routing, so every request -- known route, wrong method, or no route at all
// -- gets a kind before the inner handler runs.
func kindForPath(path string) string {
	switch path {
	case "/comment":
		return string(signalwire.KindComment)
	case "/pr-intent":
		return string(signalwire.KindPRIntent)
	case "/issue-intent":
		return string(signalwire.KindIssueIntent)
	case "/status":
		return statusKind
	default:
		return unknownKind
	}
}

// mirror wraps next so every request passes through exactly one emission
// point: it stashes a fresh opRecord on the request's context, calls next,
// then writes the one claude.SpindriftOp the record accumulated. This is what
// makes "exactly one event per request" structural rather than a convention
// each route arm has to remember.
func (h *handler) mirror(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// decision starts "reject", not "": FormatSpindriftOp renders an
		// unrecognised decision, "" included, through its accept arm, so a
		// record no route arm touches must never render as one.
		rec := &opRecord{kind: kindForPath(r.URL.Path), decision: "reject"}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), opRecordKey{}, rec)))
		h.emit(claude.SpindriftOp{
			Op:       "signal",
			Kind:     rec.kind,
			Decision: rec.decision,
			Reason:   rec.reason,
			Size:     rec.size,
			Hash:     rec.hash,
		})
	})
}

func (h *handler) emit(op claude.SpindriftOp) {
	if h.logw == nil {
		return
	}
	line := claude.EncodeSpindriftOp(op)
	if line == "" {
		return
	}
	h.logMu.Lock()
	defer h.logMu.Unlock()
	_, _ = io.WriteString(h.logw, line)
}

// serve runs one POST route: bounded read, strict decode into content, then
// accept. Content is the request type itself -- no per-route wrapper -- so a
// destination field is unrepresentable rather than merely ignored.
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
	// The receipt describes content, computed the same way whether accept
	// then buffers it or a buffer-level check (oversize body, empty, invalid
	// utf-8 field, unknown type, kind-not-consumed, intent cap) refuses it --
	// the body decoded cleanly either way, so size and hash are known here.
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
	// A GET is neither an accept nor a reject -- it never sends content for
	// the buffer to store -- so it gets its own disposition rather than
	// reusing "accept" for a request the buffer never mediated.
	rec.decision = "read"
	writeJSON(w, http.StatusOK, h.buf.Status())
}

// unknownRoute answers any path outside the four routes. It never learns a
// receipt -- there is no accept to run -- so size and hash stay zero.
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
		return invalidUTF8Reject("request body")
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

// reject records rej's decision and reason onto rec -- the mirror middleware
// reads them back once the request finishes -- and writes rej as the reply.
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
