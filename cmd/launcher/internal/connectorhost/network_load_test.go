package connectorhost_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
)

const (
	netReqOff  = 12288
	netRespOff = 16384
	survived   = `{"result":"survived"}`
)

// httpCallGuest is a network Connector whose connector_call sends req through
// spindrift.http_request(reqOff, len(req), respPtr, respCap). With pass, it
// returns the response buffer as its own envelope, so the Call result is the
// body exactly as the guest saw it; without, it drops the host's answer and
// returns a fixed envelope.
func httpCallGuest(req string, respPtr, respCap int64, pass bool) []byte {
	if pass {
		return httpCallGuestAnswering(req, respPtr, respCap, "")
	}
	return httpCallGuestAnswering(req, respPtr, respCap, survived)
}

// httpCallGuestAnswering is httpCallGuest whose fixed envelope is answer; an
// empty answer passes the host's answer through instead.
func httpCallGuestAnswering(req string, respPtr, respCap int64, answer string) []byte {
	return httpCallsGuest(answer, guestRequest{req, respPtr, respCap})
}

// guestRequest is one http_request a guest makes: the request bytes and the
// response buffer it names.
type guestRequest struct {
	req              string
	respPtr, respCap int64
}

// httpCallsGuest is a network Connector whose connector_call makes reqs in
// order and drops every answer but the last. An empty answer passes the last
// answer through as the envelope; otherwise the fixed envelope answer is
// returned.
func httpCallsGuest(answer string, reqs ...guestRequest) []byte {
	hs, fns := connectorFns([]string{"network"}, allocOff, 0)
	data := []dataSeg{{0, hs}, {respOff, []byte(answer)}}
	var body []byte
	off := int64(netReqOff)
	for i, r := range reqs {
		data = append(data, dataSeg{int(off), []byte(r.req)})
		body = append(append(append(append(body, i32Const(off)...), i32Const(int64(len(r.req)))...), i32Const(r.respPtr)...), i32Const(r.respCap)...)
		body = append(body, 0x10, 0x00)
		off += int64(len(r.req))
		if i < len(reqs)-1 {
			body = append(body, 0x1a)
		}
	}
	if answer == "" {
		body = append(append(body, i64Const(0xffffffff)...), 0x83)
		body = append(append(body, i64Const(reqs[len(reqs)-1].respPtr<<32)...), 0x84)
	} else {
		body = append(append(body, 0x1a), i64Const(respOff<<32|int64(len(answer)))...)
	}
	fns[fnCall].body = body
	imp := guestImport{"spindrift", "http_request", []byte{i32, i32, i32, i32}, []byte{i64}}
	return guestModuleWith([]guestImport{imp}, data, fns...)
}

type netLoader func(context.Context, []byte, connectorhost.Network) (*connectorhost.Connector, error)

// netLoadersFor are the ways to load a network Connector; a grant behaves the
// same under the interpreter and the compiler.
func netLoadersFor(t testing.TB) map[string]netLoader {
	return map[string]netLoader{
		"remote":      remoteNetLoader(nil),
		"process":     networkProcessLoader(t),
		"interpreter": connectorhost.LoadNetwork,
		"compiled": func(ctx context.Context, wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error) {
			return connectorhost.LoadNetworkCompiled(ctx, wasm, n, "")
		},
	}
}

func loadNet(t *testing.T, wasm []byte, n *connectorhost.Network) *connectorhost.Connector {
	t.Helper()
	return loadNetWith(t, connectorhost.LoadNetwork, wasm, n)
}

func loadNetWith(t *testing.T, load netLoader, wasm []byte, n *connectorhost.Network) *connectorhost.Connector {
	t.Helper()
	c, err := load(context.Background(), wasm, *n)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

func TestLoadNetworkGrantsHttpRequestAndKeepsTheCredentialOutOfTheGuest(t *testing.T) {
	for name, load := range netLoadersFor(t) {
		t.Run(name, func(t *testing.T) { grantsHTTPRequest(t, load) })
	}
}

func grantsHTTPRequest(t *testing.T, load netLoader) {
	b := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"result":{"authorized":%t}}`, r.Header.Get("Authorization") == "Bearer s3cret")
	})
	var audit bytes.Buffer
	req := fmt.Sprintf(`{"method":"GET","url":"%s/repos/x","headers":{"Authorization":"Bearer guest"}}`, b.URL)
	wasm := httpCallGuest(req, netRespOff, 4096, true)
	if bytes.Contains(wasm, []byte("s3cret")) {
		t.Fatal("credential in the module bytes")
	}
	c := loadNetWith(t, load, wasm, netFor(b, &audit, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"}))
	var out struct{ Authorized bool }
	if err := c.Call(context.Background(), "m", nil, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Authorized || b.hits.Load() != 1 {
		t.Fatalf("authorized = %v, hits = %d: want the host credential on one request", out.Authorized, b.hits.Load())
	}
	lines := auditLines(t, &audit)
	if len(lines) != 1 || lines[0].Decision != connectorhost.DecisionSent || lines[0].Status != 200 {
		t.Fatalf("audit = %+v, want one sent entry", lines)
	}
	if strings.Contains(audit.String(), "s3cret") {
		t.Error("credential in the audit log")
	}
}

func TestNetworkConnectorRefusalFailsClosedWithoutSending(t *testing.T) {
	for name, load := range netLoadersFor(t) {
		t.Run(name, func(t *testing.T) { refusalFailsClosed(t, load) })
	}
}

func refusalFailsClosed(t *testing.T, load netLoader) {
	b := newBackend(t, func(w http.ResponseWriter, r *http.Request) {})
	route := connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"}
	cases := map[string]string{
		"undeclared path":   fmt.Sprintf(`{"method":"GET","url":"%s/admin"}`, b.URL),
		"undeclared method": fmt.Sprintf(`{"method":"DELETE","url":"%s/repos"}`, b.URL),
		"undeclared host":   `{"method":"GET","url":"https://other.example.com/repos"}`,
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			var audit bytes.Buffer
			c := loadNetWith(t, load, httpCallGuest(req, netRespOff, 4096, false), netFor(b, &audit, route))
			before := b.hits.Load()
			err := c.Call(context.Background(), "poke", nil, nil)
			if !errors.Is(err, connectorhost.ErrCall) {
				t.Fatalf("err = %v, want ErrCall", err)
			}
			assertOnlySentinel(t, err, connectorhost.ErrRouteRefused)
			for _, want := range []string{"demo", "poke"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
			if b.hits.Load() != before {
				t.Error("a refused request reached the backend")
			}
			lines := auditLines(t, &audit)
			if len(lines) != 1 || lines[0].Decision != connectorhost.DecisionRefused {
				t.Fatalf("audit = %+v, want one refused entry", lines)
			}
			if err := c.Call(context.Background(), "again", nil, nil); !errors.Is(err, connectorhost.ErrUnusable) {
				t.Errorf("call after refusal = %v, want ErrUnusable", err)
			}
		})
	}
}

func TestNetworkConnectorRefusalNamesHostAndPath(t *testing.T) {
	b := newBackend(t, func(w http.ResponseWriter, r *http.Request) {})
	var audit bytes.Buffer
	c := loadNet(t, httpCallGuest(`{"method":"GET","url":"https://other.example.com/secret"}`, netRespOff, 4096, false),
		netFor(b, &audit, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"}))
	err := c.Call(context.Background(), "poke", nil, nil)
	for _, want := range []string{"demo", "poke", "GET", "other.example.com", "/secret"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v lacks %q", err, want)
		}
	}
}

func TestNetworkConnectorOversizedResponseFailsClosed(t *testing.T) {
	b := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("x", 2048))
	})
	var audit bytes.Buffer
	req := fmt.Sprintf(`{"method":"GET","url":"%s/repos"}`, b.URL)
	c := loadNet(t, httpCallGuest(req, netRespOff, 1024, false), netFor(b, &audit, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"}))
	err := c.Call(context.Background(), "m", nil, nil)
	assertOnlySentinel(t, err, connectorhost.ErrResponseTooLarge)
	if !errors.Is(err, connectorhost.ErrCall) {
		t.Errorf("err = %v, want ErrCall", err)
	}
	if lines := auditLines(t, &audit); len(lines) != 1 || lines[0].Decision != connectorhost.DecisionFailed {
		t.Fatalf("audit = %+v, want one failed entry", lines)
	}
}

// A transport failure is the guest's to handle: the Connector stays usable.
func TestNetworkConnectorTransportFailureIsReturnedToTheGuest(t *testing.T) {
	b := newBackend(t, func(w http.ResponseWriter, r *http.Request) {})
	var audit bytes.Buffer
	n := netFor(b, &audit, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"})
	n.Authorize = func(*http.Request) error { return errors.New("no credential") }
	req := fmt.Sprintf(`{"method":"GET","url":"%s/repos"}`, b.URL)
	c := loadNet(t, httpCallGuest(req, netRespOff, 4096, false), n)
	var out string
	for i := range 2 {
		if err := c.Call(context.Background(), "m", nil, &out); err != nil || out != "survived" {
			t.Fatalf("call %d = %q, %v, want the guest's own answer", i, out, err)
		}
	}
	lines := auditLines(t, &audit)
	if len(lines) != 2 || lines[0].Decision != connectorhost.DecisionFailed || b.hits.Load() != 0 {
		t.Fatalf("audit = %+v, hits = %d, want two failed entries and no request", lines, b.hits.Load())
	}
}

// The guest sees only the fixed reason, but the error Call returns names the
// host-side cause, never the credential.
func TestNetworkConnectorSendFailureKeepsItsHostSideReason(t *testing.T) {
	const secret = "s3cret-token"
	const unset = "CONNECTOR_TOKEN is not set"
	closed := newBackend(t, func(w http.ResponseWriter, r *http.Request) {})
	closed.Close()
	for name, tc := range map[string]struct {
		authorize func(*http.Request) error
		backend   *backend
		want      string
	}{
		"credential unset": {
			authorize: func(*http.Request) error { return errors.New(unset) },
			backend:   newBackend(t, func(w http.ResponseWriter, r *http.Request) {}),
			want:      unset,
		},
		"transport": {
			authorize: func(r *http.Request) error { r.Header.Set("Authorization", "Bearer "+secret); return nil },
			backend:   closed,
			want:      "connection refused",
		},
	} {
		t.Run(name, func(t *testing.T) {
			var audit bytes.Buffer
			n := netFor(tc.backend, &audit, connectorhost.Route{Method: "GET", Host: tc.backend.host(), Path: "/repos"})
			n.Authorize = tc.authorize
			req := fmt.Sprintf(`{"method":"GET","url":"%s/repos"}`, tc.backend.URL)
			c := loadNet(t, httpCallGuestAnswering(req, netRespOff, 4096, `{"error":"upstream unavailable"}`), n)
			err := c.Call(context.Background(), "m", nil, nil)
			if !errors.Is(err, connectorhost.ErrHTTPSend) || !errors.Is(err, connectorhost.ErrCall) {
				t.Fatalf("err = %v, want ErrCall and ErrHTTPSend", err)
			}
			var ce *connectorhost.CallError
			if !errors.As(err, &ce) {
				t.Errorf("err = %v, want the guest's CallError kept in the chain", err)
			}
			if !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), secret) {
				t.Errorf("err = %v, want the reason %q and not the credential", err, tc.want)
			}
		})
	}
}

// A send failure's reason reaches Call's error when the call then fails on an
// undecodable envelope, not only when the Connector reports an error.
func TestNetworkConnectorSendFailureReasonSurvivesAFailedCall(t *testing.T) {
	const unset = "CONNECTOR_TOKEN is not set"
	b := newBackend(t, func(w http.ResponseWriter, r *http.Request) {})
	var audit bytes.Buffer
	n := netFor(b, &audit, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"})
	n.Authorize = func(*http.Request) error { return errors.New(unset) }
	req := fmt.Sprintf(`{"method":"GET","url":"%s/repos"}`, b.URL)
	c := loadNet(t, httpCallGuestAnswering(req, netRespOff, 4096, `not json`), n)
	err := c.Call(context.Background(), "m", nil, nil)
	if !errors.Is(err, connectorhost.ErrCall) || !errors.Is(err, connectorhost.ErrHTTPSend) || !strings.Contains(err.Error(), unset) {
		t.Fatalf("err = %v, want ErrCall, ErrHTTPSend and %q", err, unset)
	}
}

// Request 1 fails to send and request 2 is sent: the host cause of request 1
// must not ride on the error of a call that ends on request 2's answer.
func TestNetworkConnectorSendFailureReasonDoesNotOutliveASentRequest(t *testing.T) {
	for name, load := range netLoadersFor(t) {
		t.Run(name, func(t *testing.T) {
			const unset = "CONNECTOR_TOKEN is not set"
			b := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"error":"not found"}`)
			})
			n := netFor(b, new(bytes.Buffer),
				connectorhost.Route{Method: "GET", Host: b.host(), Path: "/down"},
				connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"})
			n.Authorize = func(r *http.Request) error {
				if r.URL.Path == "/down" {
					return errors.New(unset)
				}
				return nil
			}
			wasm := httpCallsGuest("",
				guestRequest{fmt.Sprintf(`{"method":"GET","url":"%s/down"}`, b.URL), netRespOff, 4096},
				guestRequest{fmt.Sprintf(`{"method":"GET","url":"%s/repos"}`, b.URL), netRespOff, 4096})
			err := loadNetWith(t, load, wasm, n).Call(context.Background(), "m", nil, nil)
			if !errors.Is(err, connectorhost.ErrCall) || !strings.Contains(err.Error(), "not found") {
				t.Fatalf("err = %v, want the guest's 404 error", err)
			}
			if errors.Is(err, connectorhost.ErrHTTPSend) || strings.Contains(err.Error(), "host:") || strings.Contains(err.Error(), unset) {
				t.Errorf("err = %v, want no host cause from the earlier request", err)
			}
		})
	}
}

// Pointers outside module memory fail closed and are audited once.
func TestNetworkConnectorOutOfBoundsHostCallFailsClosed(t *testing.T) {
	b := newBackend(t, func(w http.ResponseWriter, r *http.Request) {})
	req := fmt.Sprintf(`{"method":"GET","url":"%s/repos"}`, b.URL)
	for name, wasm := range map[string][]byte{
		"response pointer": httpCallGuest(req, page-16, 4096, false),
		"response cap":     httpCallGuest(req, netRespOff, page, false),
	} {
		t.Run(name, func(t *testing.T) {
			var audit bytes.Buffer
			c := loadNet(t, wasm, netFor(b, &audit, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"}))
			assertOnlySentinel(t, c.Call(context.Background(), "m", nil, nil), connectorhost.ErrMalformedOutput)
			if lines := auditLines(t, &audit); len(lines) != 1 || lines[0].Decision != connectorhost.DecisionRefused {
				t.Fatalf("audit = %+v, want one refused entry", lines)
			}
		})
	}
	if b.hits.Load() != 0 {
		t.Error("a malformed host call reached the backend")
	}
}

func TestLoadNetworkRejectsABadConfig(t *testing.T) {
	for name, load := range netLoadersFor(t) {
		t.Run(name, func(t *testing.T) { rejectsABadConfig(t, load) })
	}
}

func rejectsABadConfig(t *testing.T, load netLoader) {
	b := newBackend(t, func(w http.ResponseWriter, r *http.Request) {})
	wasm := answeringGuest(nil, `{"result":5}`)
	good := connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"}
	var audit bytes.Buffer
	for name, mutate := range map[string]func(*connectorhost.Network){
		"blanket root route": func(n *connectorhost.Network) {
			n.Routes = []connectorhost.Route{good, {Method: "GET", Host: b.host(), Path: "/"}}
		},
		"no name":  func(n *connectorhost.Network) { n.Name = "" },
		"no audit": func(n *connectorhost.Network) { n.Audit = nil },
	} {
		n := netFor(b, &audit, good)
		mutate(n)
		c, err := load(context.Background(), wasm, *n)
		if c != nil {
			_ = c.Close(context.Background())
		}
		if err == nil {
			t.Errorf("%s: load accepted the config", name)
		} else if name == "blanket root route" && !errors.Is(err, connectorhost.ErrRouteTable) {
			t.Errorf("%s: err = %v, want ErrRouteTable", name, err)
		}
	}
}

// Load grants no Host call: the same module fails to link there.
func TestLoadStillRefusesAModuleLoadNetworkAccepts(t *testing.T) {
	wasm := httpCallGuest(`{}`, netRespOff, 4096, false)
	c, err := connectorhost.Load(context.Background(), wasm)
	if c != nil {
		_ = c.Close(context.Background())
	}
	if !errors.Is(err, connectorhost.ErrHandshake) {
		t.Fatalf("err = %v, want ErrHandshake", err)
	}
}

// A result the host rejects after a sent request has no Host call to audit it,
// so the Connector's own failure is the second entry.
func TestNetworkConnectorRejectedResultIsAudited(t *testing.T) {
	b := newBackend(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "not an envelope") })
	var audit bytes.Buffer
	req := fmt.Sprintf(`{"method":"GET","url":"%s/repos"}`, b.URL)
	c := loadNet(t, httpCallGuest(req, netRespOff, 4096, true), netFor(b, &audit, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"}))
	err := c.Call(context.Background(), "m", nil, nil)
	if !errors.Is(err, connectorhost.ErrMalformedOutput) {
		t.Fatalf("err = %v, want ErrMalformedOutput", err)
	}
	lines := auditLines(t, &audit)
	if len(lines) != 2 || lines[0].Decision != connectorhost.DecisionSent {
		t.Fatalf("audit = %+v, want the sent request then one failure", lines)
	}
	if got := lines[1]; got.Decision != connectorhost.DecisionFailed || got.Connector != "demo" || got.Reason != err.Error() {
		t.Errorf("second entry = %+v, want a failed entry for demo carrying %q", got, err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestAuditLogRecordReportsAWriteFailure(t *testing.T) {
	if err := connectorhost.NewAuditLog(failingWriter{}).Record(connectorhost.AuditEntry{Connector: "c"}); err == nil {
		t.Fatal("Record on a failing writer = nil, want the write error")
	}
}

// A request the log cannot record must not hand its answer to the guest.
func TestNetworkConnectorUnrecordableRequestFailsClosed(t *testing.T) {
	b := newBackend(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"result":"x"}`) })
	n := netFor(b, new(bytes.Buffer), connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"})
	n.Audit = connectorhost.NewAuditLog(failingWriter{})
	req := fmt.Sprintf(`{"method":"GET","url":"%s/repos"}`, b.URL)
	status, body, err := n.Do(context.Background(), []byte(req), 4096)
	if status != 0 || body != nil || !errors.Is(err, connectorhost.ErrAudit) || !strings.Contains(err.Error(), "demo") {
		t.Fatalf("Do = %d, %q, %v, want status 0, no body and ErrAudit naming demo", status, body, err)
	}

	c := loadNet(t, httpCallGuest(req, netRespOff, 4096, false), n)
	err = c.Call(context.Background(), "m", nil, nil)
	if !errors.Is(err, connectorhost.ErrCall) || !errors.Is(err, connectorhost.ErrAudit) || errors.Is(err, connectorhost.ErrTrap) {
		t.Fatalf("err = %v, want ErrCall and ErrAudit, not a trap", err)
	}
	if err := c.Call(context.Background(), "m", nil, nil); !errors.Is(err, connectorhost.ErrUnusable) {
		t.Errorf("second call err = %v, want ErrUnusable", err)
	}
}
