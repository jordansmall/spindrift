package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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

// An over-limit body must error before the socket ever sees it -- on both
// sources, not just stdin -- rather than either silently truncating (a
// body-file read) or reporting a size the agent never sent.
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
// This issue does not wire the socket into a Dispatch, so there is no
// production mint site yet to pin against RUN_NONCE. What this test can
// assert: two independent signalsocket.NewSecret() mints differ from each
// other and from whatever RUN_NONCE holds, so a constant or a
// nonce-derived implementation fails. What stays unpinned until the
// Dispatch wiring lands: whether the real mint site actually reads
// RUN_NONCE at all.
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
