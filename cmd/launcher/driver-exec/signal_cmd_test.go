package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/signalsocket"
	"spindrift.dev/launcher/internal/signalwire"
)

// syncBuffer guards the mirror sink. The handler writes it from the server's
// own goroutine, so an unguarded bytes.Buffer read from the test goroutine is
// a data race even though the write always precedes the reply.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type signalServer struct {
	buf    *signalsocket.Buffer
	log    *syncBuffer
	secret string
}

func allKinds() []signalwire.Kind {
	return []signalwire.Kind{signalwire.KindComment, signalwire.KindPRIntent, signalwire.KindIssueIntent}
}

// startSignalServer stands up a real Buffer behind a real NewHandler behind a
// real Listener and points the verb's environment at it. Nothing here is
// stubbed: the point of this harness is that the verb, the transport, the
// handler and the buffer agree end to end.
func startSignalServer(t *testing.T, transport string, cfg signalsocket.Config) *signalServer {
	t.Helper()
	buf := signalsocket.New(cfg)
	log := &syncBuffer{}

	srv := &signalServer{buf: buf, log: log}
	switch transport {
	case "unix":
		l := &signalsocket.Listener{Handler: signalsocket.NewHandler(buf, log)}
		t.Cleanup(func() { _ = l.Close() })
		// A name shorter than the helper's own "probe.sock" probe, so its
		// sun_path check stays conservative for this socket too.
		path := filepath.Join(testSocketDir(t), "sig.sock")
		if err := l.ListenAndServe(path); err != nil {
			t.Fatalf("ListenAndServe(%q): %v", path, err)
		}
		t.Setenv("SIGNAL_SOCKET_ENDPOINT", "unix://"+path)
		t.Setenv("SIGNAL_SOCKET_SECRET", "")
	case "tcp":
		secret, err := signalsocket.NewSecret()
		if err != nil {
			t.Fatalf("NewSecret: %v", err)
		}
		gh, err := signalsocket.NewGatedHandler(buf, log, secret)
		if err != nil {
			t.Fatalf("NewGatedHandler: %v", err)
		}
		l := &signalsocket.Listener{Handler: gh}
		t.Cleanup(func() { _ = l.Close() })
		if err := l.ListenAndServeTCP("127.0.0.1:0"); err != nil {
			t.Fatalf("ListenAndServeTCP: %v", err)
		}
		t.Setenv("SIGNAL_SOCKET_ENDPOINT", "http://"+l.Addr().String())
		t.Setenv("SIGNAL_SOCKET_SECRET", secret)
		srv.secret = secret
	default:
		t.Fatalf("unknown transport %q", transport)
	}
	return srv
}

func runVerb(t *testing.T, stdin string, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	rc := runSignal(args, strings.NewReader(stdin), &out)
	return rc, out.String()
}

func forEachTransport(t *testing.T, f func(t *testing.T, transport string)) {
	t.Helper()
	for _, transport := range []string{"unix", "tcp"} {
		t.Run(transport, func(t *testing.T) { f(t, transport) })
	}
}

func TestIsSignalInvocation(t *testing.T) {
	if isSignalInvocation(nil) {
		t.Fatal("isSignalInvocation(nil) = true, want false")
	}
	if !isSignalInvocation([]string{"signal", "comment"}) {
		t.Fatal("isSignalInvocation([signal comment]) = false, want true")
	}
	if isSignalInvocation([]string{"bind-registry"}) {
		t.Fatal("isSignalInvocation([bind-registry]) = true, want false")
	}
}

// The whole accept path over both transports: every kind is accepted, the
// printed receipt carries kind, bytes, hash and a rising sequence, and status
// reflects every prior accept.
func TestRunSignal_AcceptsEveryKindAndStatusReflectsThem(t *testing.T) {
	forEachTransport(t, func(t *testing.T, transport string) {
		srv := startSignalServer(t, transport, signalsocket.Config{Consumes: allKinds()})

		rc, out := runVerb(t, "a comment body", "comment")
		if rc != 0 {
			t.Fatalf("comment exit = %d, want 0 (out=%q)", rc, out)
		}
		for _, want := range []string{"comment", "accepted", "14 bytes", "sha256:", "sequence 1"} {
			if !strings.Contains(out, want) {
				t.Fatalf("comment output = %q, want it to contain %q", out, want)
			}
		}

		rc, out = runVerb(t, "pr body", "pr-intent", "-title", "a pr title")
		if rc != 0 {
			t.Fatalf("pr-intent exit = %d, want 0 (out=%q)", rc, out)
		}
		if !strings.Contains(out, "pr-intent") || !strings.Contains(out, "sequence 2") {
			t.Fatalf("pr-intent output = %q, want the kind and sequence 2", out)
		}

		rc, out = runVerb(t, "issue body", "issue-intent", "-title", "an issue title", "-type", "bug")
		if rc != 0 {
			t.Fatalf("issue-intent exit = %d, want 0 (out=%q)", rc, out)
		}
		if !strings.Contains(out, "issue-intent") || !strings.Contains(out, "sequence 3") {
			t.Fatalf("issue-intent output = %q, want the kind and sequence 3", out)
		}

		if got, ok := srv.buf.Comment(); !ok || got.Body != "a comment body" {
			t.Fatalf("buffered comment = %+v, %v, want the posted body", got, ok)
		}
		if got := srv.buf.IssueIntents(); len(got) != 1 || got[0].Type != "bug" {
			t.Fatalf("buffered issue intents = %+v, want one bug intent", got)
		}

		rc, out = runVerb(t, "", "status")
		if rc != 0 {
			t.Fatalf("status exit = %d, want 0 (out=%q)", rc, out)
		}
		st := srv.buf.Status()
		for _, want := range []string{st.Comment.Hash, st.PRIntent.Hash, st.IssueIntents[0].Hash} {
			if !strings.Contains(out, want) {
				t.Fatalf("status output = %q, want it to contain hash %q", out, want)
			}
		}
		ci, pi, ii := strings.Index(out, "comment"), strings.Index(out, "pr-intent"), strings.Index(out, "issue-intent")
		if !(ci < pi && pi < ii) {
			t.Fatalf("status output = %q, want a stable comment/pr-intent/issue-intent order", out)
		}
		if lines := strings.Count(strings.TrimSpace(out), "\n") + 1; lines != 3 {
			t.Fatalf("status output = %q, want one line per accepted signal", out)
		}
	})
}

// -body-file is the alternative to stdin; the body never travels on argv.
func TestRunSignal_BodyFile(t *testing.T) {
	forEachTransport(t, func(t *testing.T, transport string) {
		srv := startSignalServer(t, transport, signalsocket.Config{Consumes: allKinds()})
		path := filepath.Join(t.TempDir(), "body.md")
		if err := os.WriteFile(path, []byte("from a file"), 0o600); err != nil {
			t.Fatalf("write body file: %v", err)
		}

		if rc, out := runVerb(t, "ignored stdin", "comment", "-body-file", path); rc != 0 {
			t.Fatalf("exit = %d, want 0 (out=%q)", rc, out)
		}
		if got, _ := srv.buf.Comment(); got.Body != "from a file" {
			t.Fatalf("buffered comment body = %q, want the file's contents", got.Body)
		}
		if rc, out := runVerb(t, "", "comment", "-body-file", filepath.Join(t.TempDir(), "missing.md")); rc != 1 {
			t.Fatalf("missing body file exit = %d, want 1 (out=%q)", rc, out)
		}
	})
}

// A body past the verb's own limit must error client-side -- on both sources,
// not just stdin -- rather than either silently truncating (a body-file read)
// or reporting a size the agent never sent. Bodies under it still reach the
// socket, which enforces the smaller per-field MaxBodyBytes itself.
func TestRunSignal_BodyOverLimit(t *testing.T) {
	t.Setenv("SIGNAL_SOCKET_ENDPOINT", "unix://"+filepath.Join(t.TempDir(), "unused.sock"))
	t.Setenv("SIGNAL_SOCKET_SECRET", "")

	t.Run("body file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "big.md")
		f, err := os.Create(path)
		if err != nil {
			t.Fatalf("create body file: %v", err)
		}
		if _, err := f.Write(make([]byte, signalwire.MaxRequestBytes+1)); err != nil {
			t.Fatalf("write body file: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close body file: %v", err)
		}
		rc, out := runVerb(t, "", "comment", "-body-file", path)
		if rc != 1 {
			t.Fatalf("exit = %d, want 1 (out=%q)", rc, out)
		}
		if !strings.Contains(out, "body file") {
			t.Fatalf("output = %q, want it to name the body file", out)
		}
	})

	t.Run("stdin", func(t *testing.T) {
		big := strings.Repeat("a", signalwire.MaxRequestBytes+1)
		rc, out := runVerb(t, big, "comment")
		if rc != 1 {
			t.Fatalf("exit = %d, want 1 (out=%q)", rc, out)
		}
		if !strings.Contains(out, "stdin") {
			t.Fatalf("output = %q, want it to name stdin", out)
		}
	})
}

// Every reject class the issue pins, each distinct in status token and reason,
// each exiting non-zero with that reason printed.
func TestRunSignal_RejectClasses(t *testing.T) {
	forEachTransport(t, func(t *testing.T, transport string) {
		cases := []struct {
			name       string
			cfg        signalsocket.Config
			stdin      string
			args       []string
			wantStatus string
			wantReason string
			// pre runs before the case's own request, to set up state the
			// reject depends on (the cap, so far).
			pre func(t *testing.T)
		}{
			{
				name:       "empty body",
				cfg:        signalsocket.Config{Consumes: allKinds()},
				stdin:      "   \n",
				args:       []string{"comment"},
				wantStatus: "empty",
				wantReason: "body is empty",
			},
			{
				name:       "oversize body",
				cfg:        signalsocket.Config{Consumes: allKinds()},
				stdin:      strings.Repeat("a", signalwire.MaxBodyBytes+1),
				args:       []string{"comment"},
				wantStatus: "oversize",
				wantReason: fmt.Sprintf("%d-byte limit", signalwire.MaxBodyBytes),
			},
			{
				name:       "invalid utf-8",
				cfg:        signalsocket.Config{Consumes: allKinds()},
				stdin:      "body \xff\xfe",
				args:       []string{"comment"},
				wantStatus: "invalid_utf8",
				wantReason: "utf-8",
			},
			{
				name:       "unknown type enum",
				cfg:        signalsocket.Config{Consumes: allKinds()},
				stdin:      "issue body",
				args:       []string{"issue-intent", "-title", "t", "-type", "regression"},
				wantStatus: "invalid_type",
				wantReason: "not one of the supported issue-intent types",
			},
			{
				name:       "kind not consumed",
				cfg:        signalsocket.Config{Consumes: []signalwire.Kind{signalwire.KindComment}},
				stdin:      "pr body",
				args:       []string{"pr-intent", "-title", "t"},
				wantStatus: "kind_not_consumed",
				wantReason: "does not consume pr-intent signals",
			},
			{
				name:  "issue intent cap",
				cfg:   signalsocket.Config{Consumes: allKinds(), MaxIssueIntents: 1},
				stdin: "second body",
				args:  []string{"issue-intent", "-title", "second", "-type", "chore"},
				pre: func(t *testing.T) {
					if rc, out := runVerb(t, "first body", "issue-intent", "-title", "first", "-type", "bug"); rc != 0 {
						t.Fatalf("first intent exit = %d, want 0 (out=%q)", rc, out)
					}
				},
				wantStatus: "intent_cap",
				wantReason: "at most 1 issue intents",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				startSignalServer(t, transport, tc.cfg)
				if tc.pre != nil {
					tc.pre(t)
				}
				rc, out := runVerb(t, tc.stdin, tc.args...)
				if rc == 0 {
					t.Fatalf("exit = 0, want non-zero (out=%q)", out)
				}
				if !strings.Contains(out, tc.wantStatus) {
					t.Fatalf("output = %q, want the status token %q", out, tc.wantStatus)
				}
				if !strings.Contains(out, tc.wantReason) {
					t.Fatalf("output = %q, want the one-line reason %q", out, tc.wantReason)
				}
				if lines := strings.Count(strings.TrimSpace(out), "\n") + 1; lines != 1 {
					t.Fatalf("output = %q, want exactly one line", out)
				}
			})
		}
	})
}

// A TCP endpoint with no secret in the environment never earns anything but a
// 401, so the verb refuses before it sends; a wrong secret is refused by the
// listener's gate and still exits non-zero with a printed reason.
func TestRunSignal_TCPSecret(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		srv := startSignalServer(t, "tcp", signalsocket.Config{Consumes: allKinds()})
		t.Setenv("SIGNAL_SOCKET_SECRET", "")
		rc, out := runVerb(t, "a body", "comment")
		if rc != 1 {
			t.Fatalf("exit = %d, want 1 (out=%q)", rc, out)
		}
		if !strings.Contains(out, "SIGNAL_SOCKET_SECRET") {
			t.Fatalf("output = %q, want it to name the missing variable", out)
		}
		if srv.log.String() != "" {
			t.Fatalf("mirror = %q, want nothing: no request should have been sent", srv.log.String())
		}
	})

	t.Run("wrong", func(t *testing.T) {
		srv := startSignalServer(t, "tcp", signalsocket.Config{Consumes: allKinds()})
		t.Setenv("SIGNAL_SOCKET_SECRET", strings.Repeat("0", len(srv.secret)))
		rc, out := runVerb(t, "a body", "comment")
		if rc != 1 {
			t.Fatalf("exit = %d, want 1 (out=%q)", rc, out)
		}
		// The gate now rejects through the socket's own {status, reason}
		// shape (issue #3724), so the verb prints that reason rather than a
		// synthesised http_401.
		if !strings.Contains(out, "unauthorized") {
			t.Fatalf("output = %q, want the socket's own unauthorized status", out)
		}
		if !strings.Contains(out, "missing or wrong tcp secret") {
			t.Fatalf("output = %q, want the socket's own reason", out)
		}
		if _, ok := srv.buf.Comment(); ok {
			t.Fatal("a comment was buffered behind a wrong secret")
		}
	})

	// The unix transport is gated by the socket file's permissions, so the
	// secret must not travel on it even when one is in the environment.
	t.Run("never sent over unix", func(t *testing.T) {
		startSignalServer(t, "unix", signalsocket.Config{Consumes: allKinds()})
		t.Setenv("SIGNAL_SOCKET_SECRET", "a-secret-that-must-not-travel")
		if rc, out := runVerb(t, "a body", "comment"); rc != 0 {
			t.Fatalf("exit = %d, want 0 (out=%q)", rc, out)
		}
	})
}

// The per-run signal secret and RUN_NONCE are separate credentials: the Box
// sees both, and neither may be derivable from the other. internal/dispatch's
// newNonce is unexported, so the nonce is asserted here as the Box receives
// it -- through RUN_NONCE.
//
// With no production mint site until the Dispatch wiring lands, this pins
// NewSecret alone: two mints differ from each other and from RUN_NONCE, so a
// constant or nonce-derived implementation fails.
func TestRunSignal_SecretIsNotTheRunNonce(t *testing.T) {
	t.Setenv("RUN_NONCE", "0123456789abcdef0123456789abcdef")
	srv := startSignalServer(t, "tcp", signalsocket.Config{Consumes: allKinds()})
	if srv.secret == os.Getenv("RUN_NONCE") {
		t.Fatal("the listener's secret is the run nonce")
	}
	other, err := signalsocket.NewSecret()
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	if other == srv.secret {
		t.Fatal("two mints produced the same secret")
	}
	if other == os.Getenv("RUN_NONCE") {
		t.Fatal("a second mint is the run nonce")
	}
}

// Replace/dedup/append semantics, asserted through the verb rather than the
// buffer's own API.
func TestRunSignal_ReplaceDedupAppend(t *testing.T) {
	forEachTransport(t, func(t *testing.T, transport string) {
		srv := startSignalServer(t, transport, signalsocket.Config{Consumes: allKinds()})

		for _, body := range []string{"first", "second"} {
			if rc, out := runVerb(t, body, "comment"); rc != 0 {
				t.Fatalf("comment %q exit = %d, want 0 (out=%q)", body, rc, out)
			}
		}
		if got, _ := srv.buf.Comment(); got.Body != "second" {
			t.Fatalf("buffered comment = %q, want the second to replace the first", got.Body)
		}

		for _, title := range []string{"first pr", "second pr"} {
			if rc, out := runVerb(t, "pr body", "pr-intent", "-title", title); rc != 0 {
				t.Fatalf("pr-intent %q exit = %d, want 0 (out=%q)", title, rc, out)
			}
		}
		if got, _ := srv.buf.PRIntent(); got.Title != "second pr" {
			t.Fatalf("buffered pr intent title = %q, want the second to replace the first", got.Title)
		}

		for range 2 {
			if rc, out := runVerb(t, "issue body", "issue-intent", "-title", "same", "-type", "bug"); rc != 0 {
				t.Fatalf("issue-intent exit = %d, want 0 (out=%q)", rc, out)
			}
		}
		if got := srv.buf.IssueIntents(); len(got) != 1 {
			t.Fatalf("issue intents = %+v, want an identical repeat not to duplicate", got)
		}
		if rc, out := runVerb(t, "different body", "issue-intent", "-title", "other", "-type", "chore"); rc != 0 {
			t.Fatalf("second distinct issue-intent exit = %d, want 0 (out=%q)", rc, out)
		}
		if got := srv.buf.IssueIntents(); len(got) != 2 {
			t.Fatalf("issue intents = %+v, want a different intent to append", got)
		}
	})
}

// TestRunSignal_IssueIntentDedupTerms pins -dedup's repeatability (issue
// #3609): each occurrence appends, and the terms arrive in the buffered
// intent's DedupTerms in the order given.
func TestRunSignal_IssueIntentDedupTerms(t *testing.T) {
	forEachTransport(t, func(t *testing.T, transport string) {
		srv := startSignalServer(t, transport, signalsocket.Config{Consumes: allKinds()})

		if rc, out := runVerb(t, "one body", "issue-intent", "-title", "one", "-type", "bug", "-dedup", "a.go:Foo"); rc != 0 {
			t.Fatalf("issue-intent exit = %d, want 0 (out=%q)", rc, out)
		}
		if rc, out := runVerb(t, "two body", "issue-intent", "-title", "two", "-type", "chore",
			"-dedup", "a.go:Foo", "-dedup", "b.go:Bar"); rc != 0 {
			t.Fatalf("issue-intent exit = %d, want 0 (out=%q)", rc, out)
		}
		if rc, out := runVerb(t, "none body", "issue-intent", "-title", "none", "-type", "chore"); rc != 0 {
			t.Fatalf("issue-intent exit = %d, want 0 (out=%q)", rc, out)
		}

		got := srv.buf.IssueIntents()
		if len(got) != 3 {
			t.Fatalf("issue intents = %+v, want 3", got)
		}
		if want := []string{"a.go:Foo"}; !reflect.DeepEqual(got[0].DedupTerms, want) {
			t.Errorf("intent[0].DedupTerms = %v, want %v", got[0].DedupTerms, want)
		}
		if want := []string{"a.go:Foo", "b.go:Bar"}; !reflect.DeepEqual(got[1].DedupTerms, want) {
			t.Errorf("intent[1].DedupTerms = %v, want %v", got[1].DedupTerms, want)
		}
		if len(got[2].DedupTerms) != 0 {
			t.Errorf("intent[2].DedupTerms = %v, want none", got[2].DedupTerms)
		}
	})
}

// TestRunSignal_IssueIntentClassAndConcurrence pins -class/-concurrence
// (issue #3880): both are optional, and each lands in the buffered intent's
// matching field unaltered.
func TestRunSignal_IssueIntentClassAndConcurrence(t *testing.T) {
	forEachTransport(t, func(t *testing.T, transport string) {
		srv := startSignalServer(t, transport, signalsocket.Config{Consumes: allKinds()})

		if rc, out := runVerb(t, "one body", "issue-intent", "-title", "one", "-type", "bug"); rc != 0 {
			t.Fatalf("issue-intent exit = %d, want 0 (out=%q)", rc, out)
		}
		if rc, out := runVerb(t, "two body", "issue-intent", "-title", "two", "-type", "chore",
			"-class", "flaky-test"); rc != 0 {
			t.Fatalf("issue-intent exit = %d, want 0 (out=%q)", rc, out)
		}
		if rc, out := runVerb(t, "three body", "issue-intent", "-title", "three", "-type", "chore",
			"-class", "flaky-test", "-concurrence", "confirmed by the reviewer"); rc != 0 {
			t.Fatalf("issue-intent exit = %d, want 0 (out=%q)", rc, out)
		}

		got := srv.buf.IssueIntents()
		if len(got) != 3 {
			t.Fatalf("issue intents = %+v, want 3", got)
		}
		if got[0].Class != "" || got[0].Concurrence != "" {
			t.Errorf("intent[0] = %+v, want no class or concurrence", got[0])
		}
		if got[1].Class != "flaky-test" || got[1].Concurrence != "" {
			t.Errorf("intent[1] = %+v, want class flaky-test and no concurrence", got[1])
		}
		if got[2].Class != "flaky-test" || got[2].Concurrence != "confirmed by the reviewer" {
			t.Errorf("intent[2] = %+v, want class flaky-test and the concurrence", got[2])
		}
	})
}

// TestRunSignal_IssueIntentDedupWireBody pins the wire shape directly,
// bypassing the socket: -dedup adds a dedupTerms array with the given terms
// in order, and its absence must leave the posted body byte-identical to
// what it was before this field existed -- no dedupTerms key at all, not an
// empty one.
func TestRunSignal_IssueIntentDedupWireBody(t *testing.T) {
	post := func(t *testing.T, args ...string) string {
		t.Helper()
		var captured []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var err error
			captured, err = io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read posted body: %v", err)
			}
			if err := json.NewEncoder(w).Encode(signalwire.Receipt{Kind: signalwire.KindIssueIntent}); err != nil {
				t.Fatalf("encode receipt: %v", err)
			}
		}))
		t.Cleanup(srv.Close)
		t.Setenv("SIGNAL_SOCKET_ENDPOINT", srv.URL)
		t.Setenv("SIGNAL_SOCKET_SECRET", "secret")
		if rc, out := runVerb(t, "body", args...); rc != 0 {
			t.Fatalf("issue-intent exit = %d, want 0 (out=%q)", rc, out)
		}
		return string(captured)
	}

	t.Run("no dedup", func(t *testing.T) {
		got := post(t, "issue-intent", "-title", "t", "-type", "bug")
		if strings.Contains(got, "dedupTerms") {
			t.Errorf("posted body = %s, want no dedupTerms key at all", got)
		}
	})
	t.Run("one occurrence", func(t *testing.T) {
		got := post(t, "issue-intent", "-title", "t", "-type", "bug", "-dedup", "a.go:Foo")
		if !strings.Contains(got, `"dedupTerms":["a.go:Foo"]`) {
			t.Errorf("posted body = %s, want a dedupTerms array with the one term", got)
		}
	})
	t.Run("two occurrences", func(t *testing.T) {
		got := post(t, "issue-intent", "-title", "t", "-type", "bug", "-dedup", "a.go:Foo", "-dedup", "b.go:Bar")
		if !strings.Contains(got, `"dedupTerms":["a.go:Foo","b.go:Bar"]`) {
			t.Errorf("posted body = %s, want a dedupTerms array with both terms in order", got)
		}
	})
	t.Run("no class or concurrence", func(t *testing.T) {
		got := post(t, "issue-intent", "-title", "t", "-type", "bug")
		if strings.Contains(got, "class") || strings.Contains(got, "concurrence") {
			t.Errorf("posted body = %s, want no class or concurrence key at all", got)
		}
	})
	t.Run("class only", func(t *testing.T) {
		got := post(t, "issue-intent", "-title", "t", "-type", "bug", "-class", "flaky-test")
		if !strings.Contains(got, `"class":"flaky-test"`) {
			t.Errorf("posted body = %s, want a class field", got)
		}
		if strings.Contains(got, "concurrence") {
			t.Errorf("posted body = %s, want no concurrence key without -concurrence", got)
		}
	})
	t.Run("class and concurrence", func(t *testing.T) {
		got := post(t, "issue-intent", "-title", "t", "-type", "bug", "-class", "flaky-test", "-concurrence", "confirmed")
		if !strings.Contains(got, `"class":"flaky-test"`) {
			t.Errorf("posted body = %s, want a class field", got)
		}
		if !strings.Contains(got, `"concurrence":"confirmed"`) {
			t.Errorf("posted body = %s, want a concurrence field", got)
		}
	})
}

// One spindrift_op event per request reaches the log sink, carrying kind, size,
// hash and the accept/reject decision.
func TestRunSignal_MirrorsOneOpPerRequest(t *testing.T) {
	forEachTransport(t, func(t *testing.T, transport string) {
		srv := startSignalServer(t, transport, signalsocket.Config{Consumes: allKinds()})

		if rc, out := runVerb(t, "a comment body", "comment"); rc != 0 {
			t.Fatalf("comment exit = %d, want 0 (out=%q)", rc, out)
		}
		if rc, _ := runVerb(t, "", "comment"); rc == 0 {
			t.Fatal("empty comment exit = 0, want non-zero")
		}
		if rc, out := runVerb(t, "", "status"); rc != 0 {
			t.Fatalf("status exit = %d, want 0 (out=%q)", rc, out)
		}

		var ops []claude.SpindriftOp
		for _, line := range strings.Split(strings.TrimSpace(srv.log.String()), "\n") {
			var ev claude.Event
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				t.Fatalf("mirror line %q is not an event: %v", line, err)
			}
			if ev.Type != "spindrift_op" || ev.SpindriftOp == nil {
				t.Fatalf("mirror line %q is not a spindrift_op", line)
			}
			ops = append(ops, *ev.SpindriftOp)
		}
		if len(ops) != 3 {
			t.Fatalf("mirrored %d ops, want one per request: %+v", len(ops), ops)
		}
		if ops[0].Op != "signal" || ops[0].Kind != "comment" || ops[0].Decision != "accept" || ops[0].Size != 14 || !strings.HasPrefix(ops[0].Hash, "sha256:") {
			t.Fatalf("accept op = %+v, want kind, size, hash and accept", ops[0])
		}
		if ops[1].Decision != "reject" || ops[1].Reason == "" {
			t.Fatalf("reject op = %+v, want a reject decision with a reason", ops[1])
		}
		if ops[2].Kind != "status" {
			t.Fatalf("status op = %+v, want the status kind", ops[2])
		}
	})
}

// No route accepts a destination. The verb has no flag that could name one, and
// the handler refuses the fields outright rather than dropping them, so a Box
// hand-rolling the request cannot smuggle one either.
func TestRunSignal_NoRouteAcceptsADestination(t *testing.T) {
	forEachTransport(t, func(t *testing.T, transport string) {
		srv := startSignalServer(t, transport, signalsocket.Config{Consumes: allKinds()})

		for _, flagArg := range []string{"-issue", "-label", "-branch", "-target"} {
			if rc, _ := runVerb(t, "a body", "comment", flagArg, "7"); rc == 0 {
				t.Fatalf("comment %s exit = 0, want a usage failure", flagArg)
			}
		}

		client, base, err := signalClient(os.Getenv("SIGNAL_SOCKET_ENDPOINT"))
		if err != nil {
			t.Fatalf("signalClient: %v", err)
		}
		req, err := http.NewRequest(http.MethodPost, base+"/comment", strings.NewReader(`{"body":"x","issue":7,"labels":["bug"],"branch":"main"}`))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		if secret := os.Getenv("SIGNAL_SOCKET_SECRET"); secret != "" {
			req.Header.Set(signalwire.SecretHeader, secret)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: destination fields must be refused, not dropped", resp.StatusCode)
		}
		if _, ok := srv.buf.Comment(); ok {
			t.Fatal("a comment carrying a destination was buffered")
		}
	})
}

func TestRunSignal_UsageErrors(t *testing.T) {
	startSignalServer(t, "unix", signalsocket.Config{Consumes: allKinds()})

	t.Run("unknown kind", func(t *testing.T) {
		rc, out := runVerb(t, "", "nope")
		if rc != 1 {
			t.Fatalf("exit = %d, want 1 (out=%q)", rc, out)
		}
		for _, kind := range []string{"comment", "pr-intent", "issue-intent", "status"} {
			if !strings.Contains(out, kind) {
				t.Fatalf("output = %q, want the usage line to name %q", out, kind)
			}
		}
	})

	t.Run("no kind", func(t *testing.T) {
		if rc, out := runVerb(t, ""); rc != 1 {
			t.Fatalf("exit = %d, want 1 (out=%q)", rc, out)
		}
	})

	t.Run("pr-intent without title", func(t *testing.T) {
		if rc, out := runVerb(t, "a body", "pr-intent"); rc != 1 {
			t.Fatalf("exit = %d, want 1 (out=%q)", rc, out)
		}
	})

	t.Run("issue-intent without type", func(t *testing.T) {
		if rc, out := runVerb(t, "a body", "issue-intent", "-title", "t"); rc != 1 {
			t.Fatalf("exit = %d, want 1 (out=%q)", rc, out)
		}
	})
}

func TestRunSignal_EndpointErrors(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		want     string
	}{
		{name: "unset", endpoint: "", want: "SIGNAL_SOCKET_ENDPOINT"},
		{name: "unsupported scheme", endpoint: "https://example.com", want: "SIGNAL_SOCKET_ENDPOINT"},
		{name: "bare path", endpoint: "/tmp/signal.sock", want: "SIGNAL_SOCKET_ENDPOINT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SIGNAL_SOCKET_ENDPOINT", tc.endpoint)
			rc, out := runVerb(t, "a body", "comment")
			if rc != 1 {
				t.Fatalf("exit = %d, want 1 (out=%q)", rc, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("output = %q, want it to name %q", out, tc.want)
			}
			if lines := strings.Count(strings.TrimSpace(out), "\n") + 1; lines != 1 {
				t.Fatalf("output = %q, want exactly one line", out)
			}
		})
	}
}

// Nothing listening is a transport failure, not a reject: still one line, still
// non-zero.
func TestRunSignal_NothingListening(t *testing.T) {
	t.Setenv("SIGNAL_SOCKET_ENDPOINT", "unix://"+filepath.Join(testSocketDir(t), "absent.sock"))
	t.Setenv("SIGNAL_SOCKET_SECRET", "")
	rc, out := runVerb(t, "a body", "comment")
	if rc != 1 {
		t.Fatalf("exit = %d, want 1 (out=%q)", rc, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("output is empty, want a one-line failure")
	}
	if lines := strings.Count(strings.TrimSpace(out), "\n") + 1; lines != 1 {
		t.Fatalf("output = %q, want exactly one line", out)
	}
}

// lastNonEmptyLine returns s's final line with trailing whitespace ignored --
// a report's own line, if any, is printed before the rejected line
// (signalUsageFailure's contract), so this is what a `| tail -1` consumer
// would see too.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}

// TestRunSignal_UsageFailureRejectedLine pins issue #3867's client-side
// contract: every usage failure's last line is "signal <kind> rejected
// (usage): <reason>", in the same shape as a server rejection, whatever kind
// word (or none, or an unrecognised one) the agent gave.
func TestRunSignal_UsageFailureRejectedLine(t *testing.T) {
	startSignalServer(t, "unix", signalsocket.Config{Consumes: allKinds()})

	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "comment unknown flag",
			args: []string{"comment", "-nonce", "x"},
			want: "signal comment rejected (usage): flag provided but not defined: -nonce",
		},
		{
			name: "pr-intent unknown flag",
			args: []string{"pr-intent", "-nonce", "x"},
			want: "signal pr-intent rejected (usage): flag provided but not defined: -nonce",
		},
		{
			name: "issue-intent unknown flag",
			args: []string{"issue-intent", "-nonce", "x"},
			want: "signal issue-intent rejected (usage): flag provided but not defined: -nonce",
		},
		{
			name: "status unknown flag",
			args: []string{"status", "-nonce", "x"},
			want: "signal status rejected (usage): flag provided but not defined: -nonce",
		},
		{
			name: "pr-intent missing title",
			args: []string{"pr-intent"},
			want: "signal pr-intent rejected (usage): -title is required",
		},
		{
			name: "issue-intent missing type",
			args: []string{"issue-intent", "-title", "t"},
			want: "signal issue-intent rejected (usage): -type is required",
		},
		{
			name: "no args",
			args: nil,
			want: "signal rejected (usage): want one of comment, pr-intent, issue-intent, status",
		},
		{
			name: "unrecognised kind",
			args: []string{"nope"},
			want: "signal rejected (usage): unknown signal kind; want one of comment, pr-intent, issue-intent, status",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rc, out := runVerb(t, "a body", tc.args...)
			if rc == 0 {
				t.Fatalf("exit = 0, want non-zero (out=%q)", out)
			}
			if got := lastNonEmptyLine(out); got != tc.want {
				t.Fatalf("last line = %q, want %q (full out=%q)", got, tc.want, out)
			}
		})
	}
}

// TestRunSignal_UsageFailureReportsOverSocket asserts the report itself: it
// lands on the kind's usage route with the reason as its body and the TCP
// secret header, and an unrecognised kind word never reaches the wire -- it
// reports to the kindless route instead.
func TestRunSignal_UsageFailureReportsOverSocket(t *testing.T) {
	forEachTransport(t, func(t *testing.T, transport string) {
		srv := startSignalServer(t, transport, signalsocket.Config{Consumes: allKinds()})

		if rc, _ := runVerb(t, "", "comment", "-nonce", "x"); rc == 0 {
			t.Fatal("exit = 0, want non-zero")
		}
		if rc, _ := runVerb(t, "", "nope"); rc == 0 {
			t.Fatal("exit = 0, want non-zero")
		}

		var ops []claude.SpindriftOp
		for _, line := range strings.Split(strings.TrimSpace(srv.log.String()), "\n") {
			var ev claude.Event
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				t.Fatalf("mirror line %q is not an event: %v", line, err)
			}
			if ev.Type != "spindrift_op" || ev.SpindriftOp == nil {
				t.Fatalf("mirror line %q is not a spindrift_op", line)
			}
			ops = append(ops, *ev.SpindriftOp)
		}
		if len(ops) != 2 {
			t.Fatalf("mirrored %d ops, want one per usage report: %+v", len(ops), ops)
		}
		if ops[0].Decision != "usage" || ops[0].Kind != "comment" || !strings.Contains(ops[0].Reason, "-nonce") {
			t.Fatalf("comment usage op = %+v, want kind comment and the flag error as reason", ops[0])
		}
		// "unknown" is the mirror's own sentinel for the kindless route
		// (kindForPath), never the agent's unrecognised word "nope" --
		// which is what this case is really pinning: the word must not
		// reach the wire at all.
		if ops[1].Decision != "usage" || ops[1].Kind != "unknown" {
			t.Fatalf("unrecognised-kind usage op = %+v, want the kindless route, never the agent's word", ops[1])
		}
	})
}

// TestRunSignal_UsageReportTruncatesOversizeReason exercises the real
// client's truncation branch (reportSignalUsage): an unknown flag whose own
// name is long enough that "flag provided but not defined: -<name>" clears
// signalwire.MaxUsageReasonBytes. The cut lands mid multi-byte rune on
// purpose -- the flag name is built of a 3-byte character at a length that
// puts the byte-1024 cut one byte into a rune -- so this pins
// strings.ToValidUTF8 actually running on the truncated copy, not just on
// an already-clean prefix. The printed line, unlike the wire copy, keeps the
// reason whole.
func TestRunSignal_UsageReportTruncatesOversizeReason(t *testing.T) {
	srv := startSignalServer(t, "unix", signalsocket.Config{Consumes: allKinds()})

	longFlag := "-" + strings.Repeat("日", 400) // 1200 bytes, well past the 1024 cap
	rc, out := runVerb(t, "", "comment", longFlag, "x")
	if rc == 0 {
		t.Fatalf("exit = 0, want non-zero (out=%q)", out)
	}
	if got := lastNonEmptyLine(out); !strings.Contains(got, longFlag) {
		t.Fatalf("printed line = %q, want the full untruncated flag name", got)
	}

	var ops []claude.SpindriftOp
	for _, line := range strings.Split(strings.TrimSpace(srv.log.String()), "\n") {
		var ev claude.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("mirror line %q is not an event: %v", line, err)
		}
		ops = append(ops, *ev.SpindriftOp)
	}
	if len(ops) != 1 {
		t.Fatalf("mirrored %d ops, want one usage report: %+v", len(ops), ops)
	}
	op := ops[0]
	if op.Decision != "usage" || op.Kind != "comment" {
		t.Fatalf("mirrored %+v, want an accepted comment usage report", op)
	}
	if len(op.Reason) > signalwire.MaxUsageReasonBytes {
		t.Fatalf("reason is %d bytes, want at most the %d-byte cap", len(op.Reason), signalwire.MaxUsageReasonBytes)
	}
	if !utf8.ValidString(op.Reason) {
		t.Fatalf("reason %q is not valid utf-8", op.Reason)
	}
}

// TestRunSignal_UsageFailureReportNeverMasksTheError asserts the issue's
// third requirement directly: whether the report lands (a live socket) or
// cannot even be attempted (no socket at all), the usage error's own output
// and exit code are unaffected -- a failed report leaves nothing behind for
// the rejected line to follow.
func TestRunSignal_UsageFailureReportNeverMasksTheError(t *testing.T) {
	args := []string{"comment", "-nonce", "x"}

	t.Run("socket unreachable", func(t *testing.T) {
		t.Setenv("SIGNAL_SOCKET_ENDPOINT", "unix://"+filepath.Join(testSocketDir(t), "absent.sock"))
		t.Setenv("SIGNAL_SOCKET_SECRET", "")
		rc, out := runVerb(t, "", args...)
		if rc != 1 {
			t.Fatalf("exit = %d, want 1 (out=%q)", rc, out)
		}
		if got, want := lastNonEmptyLine(out), "signal comment rejected (usage): flag provided but not defined: -nonce"; got != want {
			t.Fatalf("last line = %q, want %q", got, want)
		}
	})

	t.Run("endpoint unset", func(t *testing.T) {
		t.Setenv("SIGNAL_SOCKET_ENDPOINT", "")
		t.Setenv("SIGNAL_SOCKET_SECRET", "")
		rc, out := runVerb(t, "", args...)
		if rc != 1 {
			t.Fatalf("exit = %d, want 1 (out=%q)", rc, out)
		}
		if got, want := lastNonEmptyLine(out), "signal comment rejected (usage): flag provided but not defined: -nonce"; got != want {
			t.Fatalf("last line = %q, want %q", got, want)
		}
	})
}

// TestRunSignal_HelpIsNotAUsageFailure pins the -h/-help exception: it is an
// explicit request, not a failure, so it keeps the flag package's own dump
// and exit 1 with no rejected line and no report.
func TestRunSignal_HelpIsNotAUsageFailure(t *testing.T) {
	srv := startSignalServer(t, "unix", signalsocket.Config{Consumes: allKinds()})

	rc, out := runVerb(t, "", "comment", "-h")
	if rc != 1 {
		t.Fatalf("exit = %d, want 1 (out=%q)", rc, out)
	}
	if strings.Contains(out, "rejected") {
		t.Fatalf("output = %q, want no rejected line for -h", out)
	}
	if srv.log.String() != "" {
		t.Fatalf("mirror = %q, want nothing: -h must not report", srv.log.String())
	}
}

// mainRun routes the verb and hands it stdin, so the Box's `driver-exec signal`
// invocation reaches runSignal unchanged.
func TestMainRun_RoutesSignal(t *testing.T) {
	srv := startSignalServer(t, "unix", signalsocket.Config{Consumes: allKinds()})
	stdin, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := stdin.WriteString("through mainRun"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if _, err := stdin.Seek(0, 0); err != nil {
		t.Fatalf("seek stdin: %v", err)
	}
	saved := os.Stdin
	os.Stdin = stdin
	t.Cleanup(func() { os.Stdin = saved })

	var out, errOut bytes.Buffer
	if rc := mainRun([]string{"signal", "comment"}, &out, &errOut); rc != 0 {
		t.Fatalf("mainRun exit = %d, want 0 (out=%q, stderr=%q)", rc, out.String(), errOut.String())
	}
	if got, _ := srv.buf.Comment(); got.Body != "through mainRun" {
		t.Fatalf("buffered comment = %q, want the stdin body", got.Body)
	}
}

// rawFields must marshal a wire struct to the exact same bytes a hand-built
// signalBody map would have -- keys sorted alphabetically by encoding/json,
// omitempty fields dropped when zero/empty -- and it must preserve an
// invalid UTF-8 byte unlaundered, the same guarantee rawString gives a
// hand-built map today.
func TestRawFields(t *testing.T) {
	t.Run("all fields set", func(t *testing.T) {
		got, err := json.Marshal(rawFields(signalwire.IssueIntent{
			Title:       "t",
			Body:        "b",
			Type:        "bug",
			DedupTerms:  []string{"a.go:Foo", "b.go:Bar"},
			Class:       "flaky-test",
			Concurrence: "confirmed",
		}))
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		want := `{"body":"b","class":"flaky-test","concurrence":"confirmed","dedupTerms":["a.go:Foo","b.go:Bar"],"title":"t","type":"bug"}`
		if string(got) != want {
			t.Fatalf("rawFields marshaled = %s, want %s", got, want)
		}
	})

	t.Run("omitempty fields dropped when zero", func(t *testing.T) {
		got, err := json.Marshal(rawFields(signalwire.IssueIntent{
			Title: "t",
			Body:  "b",
			Type:  "bug",
		}))
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		want := `{"body":"b","title":"t","type":"bug"}`
		if string(got) != want {
			t.Fatalf("rawFields marshaled = %s, want %s", got, want)
		}
	})

	t.Run("required fields without omitempty stay even when zero", func(t *testing.T) {
		got, err := json.Marshal(rawFields(signalwire.Comment{}))
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		want := `{"body":""}`
		if string(got) != want {
			t.Fatalf("rawFields marshaled = %s, want %s", got, want)
		}
	})

	t.Run("invalid utf-8 survives as a raw byte", func(t *testing.T) {
		got, err := json.Marshal(rawFields(signalwire.Comment{Body: "\xff"}))
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		want := "{\"body\":\"\xff\"}"
		if string(got) != want {
			t.Fatalf("rawFields marshaled = %q, want %q (raw 0xff, not U+FFFD)", got, want)
		}
		if utf8.ValidString(string(got)) {
			t.Fatalf("marshaled body is valid utf-8, want the raw invalid byte preserved")
		}
	})

	t.Run("unsupported field kind panics with the helpful message, not reflect's own", func(t *testing.T) {
		type badWire struct {
			Count int `json:"count,omitempty"`
		}
		defer func() {
			r := recover()
			msg, ok := r.(string)
			if !ok {
				t.Fatalf("recover() = %v (%T), want a string panic", r, r)
			}
			want := "rawFields: badWire.Count is int, not string or []string"
			if msg != want {
				t.Fatalf("panic = %q, want %q", msg, want)
			}
		}()
		// Count is non-zero so the old omitempty-first ordering would have
		// hit reflect.Value.Len's own panic on an int kind before ever
		// reaching the type switch.
		rawFields(badWire{Count: 1})
	})
}
