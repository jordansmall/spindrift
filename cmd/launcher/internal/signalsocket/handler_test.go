package signalsocket_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/signalsocket"
	"spindrift.dev/launcher/internal/signalwire"
)

// do serves one request against a fresh recorder and returns it.
func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decodeReject(t *testing.T, w *httptest.ResponseRecorder) signalwire.Reject {
	t.Helper()
	var rej signalwire.Reject
	if err := json.Unmarshal(w.Body.Bytes(), &rej); err != nil {
		t.Fatalf("reject reply %q: %v", w.Body.String(), err)
	}
	return rej
}

func wantReject(t *testing.T, w *httptest.ResponseRecorder, status string, code int) signalwire.Reject {
	t.Helper()
	rej := decodeReject(t, w)
	if rej.Status != status || w.Code != code {
		t.Fatalf("got %d/%q (reason %q), want %d/%q", w.Code, rej.Status, rej.Reason, code, status)
	}
	if strings.Contains(rej.Reason, "\n") {
		t.Errorf("reason %q spans more than one line", rej.Reason)
	}
	return rej
}

func TestHandlerAcceptsEveryKind(t *testing.T) {
	b := newAll(t)
	h := signalsocket.NewHandler(b, nil)

	for _, tc := range []struct {
		path string
		body string
		kind signalwire.Kind
	}{
		{"/comment", `{"body":"looks good"}`, signalwire.KindComment},
		{"/pr-intent", `{"title":"fix the thing","body":"details"}`, signalwire.KindPRIntent},
		{"/issue-intent", `{"title":"flaky test","body":"details","type":"bug"}`, signalwire.KindIssueIntent},
	} {
		w := do(t, h, http.MethodPost, tc.path, tc.body)
		if w.Code != http.StatusOK {
			t.Fatalf("POST %s = %d (%s), want 200", tc.path, w.Code, w.Body.String())
		}
		var rec signalwire.Receipt
		if err := json.Unmarshal(w.Body.Bytes(), &rec); err != nil {
			t.Fatalf("POST %s receipt %q: %v", tc.path, w.Body.String(), err)
		}
		if rec.Kind != tc.kind || rec.Bytes == 0 || !strings.HasPrefix(rec.Hash, "sha256:") || rec.Sequence == 0 {
			t.Errorf("POST %s receipt = %+v, want a complete receipt for %q", tc.path, rec, tc.kind)
		}
	}

	if c, ok := b.Comment(); !ok || c.Body != "looks good" {
		t.Errorf("buffered comment = %+v (ok=%v), want the posted body", c, ok)
	}
	if p, ok := b.PRIntent(); !ok || p.Title != "fix the thing" {
		t.Errorf("buffered pr intent = %+v (ok=%v), want the posted title", p, ok)
	}
	if got := b.IssueIntents(); len(got) != 1 || got[0].Type != "bug" {
		t.Errorf("buffered issue intents = %+v, want one bug intent", got)
	}

	w := do(t, h, http.MethodGet, "/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /status = %d (%s), want 200", w.Code, w.Body.String())
	}
	var st signalwire.Status
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("GET /status body %q: %v", w.Body.String(), err)
	}
	if st.Comment == nil || st.PRIntent == nil || len(st.IssueIntents) != 1 {
		t.Errorf("GET /status = %+v, want all three kinds reported", st)
	}
}

func TestHandlerRejectsUnknownPathWithoutEchoingIt(t *testing.T) {
	h := signalsocket.NewHandler(newAll(t), nil)
	for _, path := range []string{"/merge", "/comment/7", "/"} {
		w := do(t, h, http.MethodPost, path, `{"body":"x"}`)
		wantReject(t, w, "unknown_kind", http.StatusNotFound)
		if path != "/" && strings.Contains(w.Body.String(), strings.TrimPrefix(path, "/")) {
			t.Errorf("POST %s reply %q echoes the requested path", path, w.Body.String())
		}
	}
}

// The mirror names an unknown route "unknown", a fixed token, never the
// requested path: the path is Box-controlled, and echoing it into the
// launcher's log would let a caller write its own text there.
func TestHandlerMirrorsUnknownKindWithoutEchoingPath(t *testing.T) {
	var log strings.Builder
	h := signalsocket.NewHandler(newAll(t), &log)
	w := do(t, h, http.MethodPost, "/comments-typo-xyz", `{"body":"x"}`)
	wantReject(t, w, "unknown_kind", http.StatusNotFound)

	ops := mirroredOps(t, log.String())
	if len(ops) != 1 {
		t.Fatalf("mirrored %d ops, want one per request (1): %+v", len(ops), ops)
	}
	if ops[0].Kind != "unknown" || ops[0].Decision != "reject" {
		t.Fatalf("mirrored %+v, want an unknown-kind reject", ops[0])
	}
	if strings.Contains(log.String(), "comments-typo-xyz") {
		t.Errorf("mirror line %q echoes the requested path", log.String())
	}
}

func TestHandlerRejectsWrongMethod(t *testing.T) {
	var log strings.Builder
	h := signalsocket.NewHandler(newAll(t), &log)
	cases := []struct {
		method, path, wantKind string
	}{
		{http.MethodGet, "/comment", "comment"},
		{http.MethodGet, "/pr-intent", "pr-intent"},
		{http.MethodPut, "/issue-intent", "issue-intent"},
		{http.MethodPost, "/status", "status"},
	}
	for _, tc := range cases {
		w := do(t, h, tc.method, tc.path, "")
		wantReject(t, w, "method_not_allowed", http.StatusMethodNotAllowed)
	}

	// A wrong method still resolves from the path: the mirrored kind is the
	// route's own, not "unknown".
	ops := mirroredOps(t, log.String())
	if len(ops) != len(cases) {
		t.Fatalf("mirrored %d ops, want one per request (%d): %+v", len(ops), len(cases), ops)
	}
	for i, tc := range cases {
		if ops[i].Kind != tc.wantKind {
			t.Errorf("%s %s mirrored kind %q, want %q", tc.method, tc.path, ops[i].Kind, tc.wantKind)
		}
	}
}

func TestHandlerRejectsMalformedBody(t *testing.T) {
	h := signalsocket.NewHandler(newAll(t), nil)
	for _, body := range []string{``, `{`, `[{"body":"x"}]`, `"just a string"`, `{"body":"x"} {"body":"y"}`} {
		w := do(t, h, http.MethodPost, "/comment", body)
		wantReject(t, w, "malformed_json", http.StatusBadRequest)
	}
}

// Content is never destination (issue #3724): a request that tries to name one
// must fail loudly rather than have the field dropped on the floor.
func TestHandlerRejectsDestinationFields(t *testing.T) {
	h := signalsocket.NewHandler(newAll(t), nil)
	for _, tc := range []struct{ path, body string }{
		{"/comment", `{"body":"x","issue":7}`},
		{"/pr-intent", `{"title":"t","body":"x","branch":"main"}`},
		{"/issue-intent", `{"title":"t","body":"x","type":"bug","labels":["p1"]}`},
	} {
		w := do(t, h, http.MethodPost, tc.path, tc.body)
		wantReject(t, w, "malformed_json", http.StatusBadRequest)
	}
}

func TestHandlerRejectsOversizeBody(t *testing.T) {
	h := signalsocket.NewHandler(newAll(t), nil)

	// Over the reader's ceiling: the decoder never sees a whole value.
	huge := strings.Repeat("a", signalwire.MaxRequestBytes+1)
	for _, tc := range []struct{ path, body string }{
		{"/comment", `{"body":"` + huge + `"}`},
		{"/pr-intent", `{"title":"t","body":"` + huge + `"}`},
	} {
		w := do(t, h, http.MethodPost, tc.path, tc.body)
		wantReject(t, w, "oversize", http.StatusRequestEntityTooLarge)
	}

	// Under the reader's ceiling but over the buffer's own body limit.
	big := strings.Repeat("b", signalwire.MaxBodyBytes+1)
	for _, tc := range []struct{ path, body string }{
		{"/comment", `{"body":"` + big + `"}`},
		{"/pr-intent", `{"title":"t","body":"` + big + `"}`},
	} {
		w := do(t, h, http.MethodPost, tc.path, tc.body)
		wantReject(t, w, "oversize", http.StatusRequestEntityTooLarge)
	}
}

// A field over the buffer's own limit still decodes cleanly, so the buffer
// computes a receipt for it before refusing it -- the mirror carries that
// receipt's size and hash even though the signal never got buffered.
func TestHandlerMirrorsSizeAndHashOnAFieldOversizeReject(t *testing.T) {
	var log strings.Builder
	h := signalsocket.NewHandler(newAll(t), &log)
	body := strings.Repeat("c", signalwire.MaxBodyBytes+1)
	w := do(t, h, http.MethodPost, "/comment", `{"body":"`+body+`"}`)
	wantReject(t, w, "oversize", http.StatusRequestEntityTooLarge)

	ops := mirroredOps(t, log.String())
	if len(ops) != 1 {
		t.Fatalf("mirrored %d ops, want one per request (1): %+v", len(ops), ops)
	}
	if ops[0].Kind != "comment" || ops[0].Decision != "reject" {
		t.Fatalf("mirrored %+v, want a comment reject", ops[0])
	}
	if ops[0].Size != len(body) || !strings.HasPrefix(ops[0].Hash, "sha256:") {
		t.Errorf("mirrored %+v, want size %d and a sha256 hash of the refused body", ops[0], len(body))
	}
}

// A max-size body escapes to more bytes than it holds, so the reader's ceiling
// must leave the buffer's own limit room to be the binding one.
func TestHandlerAcceptsMaxSizeBody(t *testing.T) {
	h := signalsocket.NewHandler(newAll(t), nil)
	body, err := json.Marshal(signalwire.Comment{Body: strings.Repeat("\x01", signalwire.MaxBodyBytes)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	w := do(t, h, http.MethodPost, "/comment", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("POST /comment with a max-size escaped body = %d (%s), want 200", w.Code, w.Body.String())
	}
}

func TestHandlerRejectsKindNotConsumed(t *testing.T) {
	b := signalsocket.New(signalsocket.Config{Consumes: []signalwire.Kind{signalwire.KindComment}})
	h := signalsocket.NewHandler(b, nil)
	w := do(t, h, http.MethodPost, "/pr-intent", `{"title":"t","body":"x"}`)
	wantReject(t, w, "kind_not_consumed", http.StatusConflict)
}

func mirroredOps(t *testing.T, log string) []claude.SpindriftOp {
	t.Helper()
	var ops []claude.SpindriftOp
	for _, line := range strings.Split(strings.TrimSuffix(log, "\n"), "\n") {
		if line == "" {
			continue
		}
		var ev claude.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("mirror line %q: %v", line, err)
		}
		if ev.Type != "spindrift_op" || ev.SpindriftOp == nil {
			t.Fatalf("mirror line %q is not a spindrift_op event", line)
		}
		if ev.SpindriftOp.Op != "signal" {
			t.Fatalf("mirror line %q: op = %q, want %q", line, ev.SpindriftOp.Op, "signal")
		}
		ops = append(ops, *ev.SpindriftOp)
	}
	return ops
}

func TestHandlerMirrorsOneOpPerRequest(t *testing.T) {
	var log strings.Builder
	h := signalsocket.NewHandler(newAll(t), &log)

	do(t, h, http.MethodPost, "/comment", `{"body":"looks good"}`)
	do(t, h, http.MethodPost, "/issue-intent", `{"title":"t","body":"x","type":"chore"}`)
	do(t, h, http.MethodGet, "/status", "")
	do(t, h, http.MethodPost, "/comment", `{"body":"   "}`)
	do(t, h, http.MethodGet, "/comment", "")
	do(t, h, http.MethodPost, "/nope", `{}`)

	ops := mirroredOps(t, log.String())
	if len(ops) != 6 {
		t.Fatalf("mirrored %d ops, want one per request (6): %+v", len(ops), ops)
	}
	for i, want := range []struct {
		kind, decision string
		// hasReceipt is true wherever the request's content decoded cleanly
		// enough for the buffer to compute a receipt for it, whether that
		// receipt was then accepted or refused -- as opposed to a request
		// that never reached the buffer at all (wrong method, unknown
		// route), which carries no receipt to mirror.
		hasReceipt bool
	}{
		{"comment", "accept", true},
		{"issue-intent", "accept", true},
		{"status", "read", false},
		{"comment", "reject", true},  // empty body: decodes fine, the buffer refuses it
		{"comment", "reject", false}, // wrong method: never reaches the buffer
		{"unknown", "reject", false}, // unknown route: never reaches the buffer
	} {
		got := ops[i]
		if got.Kind != want.kind || got.Decision != want.decision {
			t.Errorf("op %d = kind %q decision %q, want %q/%q", i, got.Kind, got.Decision, want.kind, want.decision)
		}
		if want.hasReceipt && (got.Size == 0 || !strings.HasPrefix(got.Hash, "sha256:")) {
			t.Errorf("op %d = %+v, want the receipt's size and hash", i, got)
		}
		if !want.hasReceipt && (got.Size != 0 || got.Hash != "") {
			t.Errorf("op %d = %+v, want no size or hash", i, got)
		}
		if want.decision == "reject" && got.Reason == "" {
			t.Errorf("op %d = %+v, want the reject reason", i, got)
		}
		if want.decision != "reject" && got.Reason != "" {
			t.Errorf("op %d = %+v, want no reason on a non-reject", i, got)
		}
	}
}

func TestHandlerMirrorsSizeAndHashFromTheReceipt(t *testing.T) {
	var log strings.Builder
	h := signalsocket.NewHandler(newAll(t), &log)
	w := do(t, h, http.MethodPost, "/comment", `{"body":"looks good"}`)
	var rec signalwire.Receipt
	if err := json.Unmarshal(w.Body.Bytes(), &rec); err != nil {
		t.Fatalf("receipt: %v", err)
	}
	ops := mirroredOps(t, log.String())
	if len(ops) != 1 || ops[0].Size != rec.Bytes || ops[0].Hash != rec.Hash {
		t.Errorf("mirrored %+v, want size %d and hash %q from the receipt", ops, rec.Bytes, rec.Hash)
	}
}

// A reply crosses back into the Box, so it may name no path, token or issue.
func TestHandlerReplyNamesNothingOutsideTheBox(t *testing.T) {
	b := signalsocket.New(signalsocket.Config{
		Consumes:        []signalwire.Kind{signalwire.KindComment, signalwire.KindIssueIntent},
		MaxIssueIntents: 1,
	})
	h := signalsocket.NewHandler(b, io.Discard)

	reqs := []struct{ method, path, body string }{
		{http.MethodPost, "/comment", `{"body":"looks good"}`},
		{http.MethodPost, "/issue-intent", `{"title":"t","body":"x","type":"bug"}`},
		{http.MethodPost, "/issue-intent", `{"title":"u","body":"y","type":"chore"}`}, // intent_cap
		{http.MethodGet, "/status", ""},
		{http.MethodPost, "/pr-intent", `{"title":"t","body":"x"}`},                  // kind_not_consumed
		{http.MethodPost, "/comment", `{"body":"x","issue":7}`},                      // malformed_json
		{http.MethodPost, "/comment", `{"body":""}`},                                 // empty
		{http.MethodPost, "/comment", "{\"body\":\"\xff\xfe\"}"},                     // invalid utf-8 bytes
		{http.MethodPost, "/issue-intent", `{"title":"t","body":"x","type":"epic"}`}, // invalid_type
		{http.MethodPost, "/comment", `{"body":"` + strings.Repeat("c", signalwire.MaxBodyBytes+1) + `"}`},
		{http.MethodGet, "/comment", ""},
		{http.MethodPost, "/tmp/escape", `{}`},
	}
	for _, r := range reqs {
		w := do(t, h, r.method, r.path, r.body)
		got := w.Body.String()
		for _, banned := range []string{"/", "#", "tmp"} {
			if strings.Contains(strings.ToLower(got), banned) {
				t.Errorf("%s %s reply %q contains %q", r.method, r.path, got, banned)
			}
		}
	}
}

func TestHandlerNilLogWriterDiscardsTheMirror(t *testing.T) {
	h := signalsocket.NewHandler(newAll(t), nil)
	if w := do(t, h, http.MethodPost, "/comment", `{"body":"x"}`); w.Code != http.StatusOK {
		t.Fatalf("POST /comment with a nil log writer = %d, want 200", w.Code)
	}
}

// encoding/json substitutes U+FFFD for invalid bytes inside a JSON string, so
// the raw request bytes are the only place the fault is still visible: the
// check has to sit ahead of the decoder, not behind it.
func TestHandlerRejectsInvalidUTF8Bytes(t *testing.T) {
	var log strings.Builder
	h := signalsocket.NewHandler(newAll(t), &log)
	for _, tc := range []struct{ path, body string }{
		{"/comment", "{\"body\":\"a\xffb\"}"},
		{"/pr-intent", "{\"title\":\"t\",\"body\":\"a\xfeb\"}"},
		{"/issue-intent", "{\"title\":\"a\xffb\",\"body\":\"x\",\"type\":\"bug\"}"},
	} {
		w := do(t, h, http.MethodPost, tc.path, tc.body)
		wantReject(t, w, "invalid_utf8", http.StatusBadRequest)
	}

	ops := mirroredOps(t, log.String())
	if len(ops) != 3 {
		t.Fatalf("mirrored %d ops, want one per request (3): %+v", len(ops), ops)
	}
	for i, want := range []string{"comment", "pr-intent", "issue-intent"} {
		if ops[i].Kind != want || ops[i].Decision != "reject" || ops[i].Reason == "" {
			t.Errorf("op %d = %+v, want a %q reject with a reason", i, ops[i], want)
		}
	}
}

// The bounded read runs before the utf-8 check, so a body over the reader's
// ceiling still reports oversize even when its bytes are invalid utf-8 too.
func TestHandlerRejectsOversizeBeforeInvalidUTF8(t *testing.T) {
	h := signalsocket.NewHandler(newAll(t), nil)
	for _, filler := range []string{"a", "\xff"} {
		huge := strings.Repeat(filler, signalwire.MaxRequestBytes+1)
		w := do(t, h, http.MethodPost, "/comment", `{"body":"`+huge+`"}`)
		wantReject(t, w, "oversize", http.StatusRequestEntityTooLarge)
	}
}

// http.ServeMux answers a request whose path it cleans (a doubled slash, a
// dot segment, or an unregistered trailing slash) with its own 307 redirect
// and never calls a registered handler at all -- no route arm would ever set
// a decision on such a request. httptest.NewRequest("//comment") parses as a
// scheme-relative URL (host "comment", empty path), not the doubled-slash
// path a real request line carries, so these use the absolute form and rely
// on url.Parse leaving dot segments unresolved in RequestURI.
func TestHandlerRejectsPathsServeMuxWouldClean(t *testing.T) {
	for _, path := range []string{
		"http://box//comment",
		"http://box/comment/../comment",
		"http://box/comment/",
		"http://box/status/",
	} {
		t.Run(path, func(t *testing.T) {
			var log strings.Builder
			h := signalsocket.NewHandler(newAll(t), &log)
			w := do(t, h, http.MethodPost, path, `{"body":"x"}`)
			if w.Code >= 300 && w.Code < 400 {
				t.Fatalf("POST %s = %d, want a 404 reject, never a redirect", path, w.Code)
			}
			rej := wantReject(t, w, "unknown_kind", http.StatusNotFound)
			// The fixed reason unknownRoute always writes, never one built
			// from the request: "status" is a legitimate substring of that
			// reason's own JSON field name, so a raw-body substring check
			// on "comment"/"status" would false-positive on every case.
			if rej.Reason != "no such signal kind" {
				t.Errorf("POST %s reply reason %q, want the fixed unknown-route reason", path, rej.Reason)
			}

			ops := mirroredOps(t, log.String())
			if len(ops) != 1 {
				t.Fatalf("mirrored %d ops, want one per request (1): %+v", len(ops), ops)
			}
			if ops[0].Kind != "unknown" || ops[0].Decision != "reject" {
				t.Fatalf("mirrored %+v, want an unknown-kind reject", ops[0])
			}
		})
	}
}
