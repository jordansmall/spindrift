// In-package, like wire_fuzz_test.go: a scripted host speaks the wire to the
// remote engine, so frames no real host sends are testable.

package connectorhost

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptedLink is a hostLink whose host end runs script over the host's side of
// the stream; killing the host closes both pipes, which unblocks the script.
func scriptedLink(t *testing.T, script func(r io.Reader, w io.Writer)) hostLink {
	t.Helper()
	toHostR, toHostW := io.Pipe()
	fromHostR, fromHostW := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		script(toHostR, fromHostW)
	}()
	killed := errors.New("host killed")
	kill := func() { _ = toHostR.CloseWithError(killed); _ = fromHostR.CloseWithError(killed) }
	t.Cleanup(func() { kill(); <-done })
	return hostLink{r: fromHostR, w: toHostW, kill: kill, wait: func() error { <-done; return nil }}
}

func grantedEngine(t *testing.T, audit *bytes.Buffer, script func(r io.Reader, w io.Writer)) *remoteEngine {
	t.Helper()
	return &remoteEngine{
		link: scriptedLink(t, script),
		host: &networkCalls{n: &Network{Name: "scripted", Audit: NewAuditLog(audit)}},
	}
}

func send(t *testing.T, w io.Writer, kind byte, parts ...[]byte) {
	t.Helper()
	if err := writeFrame(w, kind, parts...); err != nil {
		t.Errorf("host write: %v", err)
	}
}

func expect(t *testing.T, r io.Reader, want byte) {
	t.Helper()
	if kind, _, err := readFrame(r); err != nil || kind != want {
		t.Errorf("host read kind %d, %v, want %d", kind, err, want)
	}
}

func refusePayload(call, why string) [][]byte {
	return [][]byte{u32(len(call)), []byte(call), []byte(why)}
}

func TestRemoteHostCannotSucceedAfterAFailedHostCall(t *testing.T) {
	cases := map[string]func(t *testing.T, r io.Reader, w io.Writer){
		"refuse then result": func(t *testing.T, r io.Reader, w io.Writer) {
			expect(t, r, kindCall)
			send(t, w, kindRefuse, refusePayload(hostRequest, "request points outside module memory")...)
			send(t, w, kindResult, []byte(`{"result":1}`))
		},
		"refuse then http_request": func(t *testing.T, r io.Reader, w io.Writer) {
			expect(t, r, kindCall)
			send(t, w, kindRefuse, refusePayload(hostRequest, "x")...)
			send(t, w, kindHTTPRequest, u32(64), []byte(`{}`))
		},
		"replyFatal then result": func(t *testing.T, r io.Reader, w io.Writer) {
			expect(t, r, kindCall)
			send(t, w, kindHTTPRequest, u32(64), []byte(`{}`))
			expect(t, r, kindReplyFatal)
			send(t, w, kindResult, []byte(`{"result":1}`))
		},
		"replyFatal then http_request": func(t *testing.T, r io.Reader, w io.Writer) {
			expect(t, r, kindCall)
			send(t, w, kindHTTPRequest, u32(64), []byte(`{}`))
			expect(t, r, kindReplyFatal)
			send(t, w, kindHTTPRequest, u32(64), []byte(`{}`))
		},
	}
	for name, script := range cases {
		t.Run(name, func(t *testing.T) {
			var audit bytes.Buffer
			e := grantedEngine(t, &audit, func(r io.Reader, w io.Writer) { script(t, r, w) })
			out, err := e.call(context.Background(), []byte(`{"method":"m"}`))
			if err == nil || !errors.Is(err, ErrMalformedOutput) {
				t.Fatalf("out = %q, err = %v, want ErrMalformedOutput", out, err)
			}
			if !strings.Contains(audit.String(), `"decision":"refused"`) {
				t.Errorf("audit = %q, want the refusal it must not outlive", audit.String())
			}
		})
	}
}

func TestRemoteRefusalOfAnUnknownHostCallIsMalformedAndUnaudited(t *testing.T) {
	var audit bytes.Buffer
	e := grantedEngine(t, &audit, func(r io.Reader, w io.Writer) {
		expect(t, r, kindCall)
		send(t, w, kindRefuse, refusePayload("x\n{\"decision\":\"sent\"}", "why")...)
	})
	_, err := e.call(context.Background(), []byte(`{"method":"m"}`))
	if !errors.Is(err, ErrMalformedOutput) {
		t.Fatalf("err = %v, want ErrMalformedOutput", err)
	}
	if audit.Len() != 0 {
		t.Errorf("audit = %q, want none for a host-chosen call name", audit.String())
	}
}

func TestRefuseQuotesWhy(t *testing.T) {
	var audit bytes.Buffer
	h := &networkCalls{n: &Network{Name: "n", Audit: NewAuditLog(&audit)}}
	err := h.refuse(hostRequest, "a\nb")
	if strings.Contains(err.Error(), "a\nb") || !strings.Contains(err.Error(), `"a\nb"`) {
		t.Errorf("err = %q, want why quoted", err)
	}
}

func TestRemoteLoadIsBoundedByTheInitBudget(t *testing.T) {
	old := initLimits.budget
	initLimits.budget = 100 * time.Millisecond
	t.Cleanup(func() { initLimits.budget = old })
	e := &remoteEngine{link: scriptedLink(t, func(r io.Reader, w io.Writer) {
		expect(t, r, kindLoad)
		_, _ = io.Copy(io.Discard, r) // never answers
	})}
	start := time.Now()
	err := e.load(context.Background(), []byte("wasm"))
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("load took %v against a %v budget", d, initLimits.budget)
	}
}

func TestRemoteLoadServesHostCalls(t *testing.T) {
	var audit bytes.Buffer
	e := grantedEngine(t, &audit, func(r io.Reader, w io.Writer) {
		expect(t, r, kindLoad)
		send(t, w, kindHTTPRequest, u32(64), []byte(`{}`))
		expect(t, r, kindReplyFatal)
		send(t, w, kindFailure, []byte{byte(slices.Index(failureSentinels, errRemoteHostCall))}, []byte("host call failed"))
	})
	err := e.load(context.Background(), []byte("wasm"))
	if !errors.Is(err, ErrRouteRefused) {
		t.Fatalf("err = %v, want the served Host call's ErrRouteRefused", err)
	}
	if !strings.Contains(audit.String(), `"decision":"refused"`) {
		t.Errorf("audit = %q, want the refusal", audit.String())
	}
}

func TestRemoteLoadRefusesAModuleOverTheFrameCapByName(t *testing.T) {
	e := &remoteEngine{link: scriptedLink(t, func(r io.Reader, w io.Writer) {
		if kind, _, err := readFrame(r); err == nil {
			t.Errorf("host received a frame of kind %d for a module that cannot fit", kind)
		}
	})}
	err := e.load(context.Background(), make([]byte, maxFrame))
	if err == nil || errors.Is(err, ErrTrap) || !strings.Contains(err.Error(), "does not fit") {
		t.Fatalf("err = %v, want a plain error naming the cap, not ErrTrap", err)
	}
}

func TestRemoteResultThatRacesTheBudgetIsABudgetFailure(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	link := scriptedLink(t, func(r io.Reader, w io.Writer) {
		expect(t, r, kindCall)
		<-entered // the budget has ended and the host is being killed
		send(t, w, kindResult, []byte(`{}`))
	})
	kill := link.kill
	// Holds the kill open so the result is read after the budget ended, not
	// after the host died.
	link.kill = func() {
		once.Do(func() { close(entered) })
		<-release
		kill()
	}
	t.Cleanup(func() { close(release) })
	e := &remoteEngine{link: link}
	_, err := e.exchange(context.Background(), "connector_call", limits{budget: 10 * time.Millisecond}, kindCall, []byte(`{}`))
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget for a result that arrived after the budget", err)
	}
}
