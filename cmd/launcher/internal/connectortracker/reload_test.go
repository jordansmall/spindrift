package connectortracker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
)

type fakeConn struct {
	dead  error
	calls int
}

func (f *fakeConn) Call(context.Context, string, any, any) error { f.calls++; return nil }
func (f *fakeConn) Err() error                                   { return f.dead }

func TestReloaderReloadsADeadConnectorBeforeTheNextCall(t *testing.T) {
	first, second := &fakeConn{}, &fakeConn{}
	loads := 0
	r := &reloader{cur: first, load: func() (conn, error) { loads++; return second, nil }}
	ctx := context.Background()

	if err := r.call(ctx, "m", nil, nil); err != nil || first.calls != 1 || loads != 0 {
		t.Fatalf("live call: err=%v first=%d loads=%d, want one call on the first connector and no load", err, first.calls, loads)
	}
	first.dead = errors.New("trapped")
	if err := r.call(ctx, "m", nil, nil); err != nil || second.calls != 1 || first.calls != 1 || loads != 1 {
		t.Fatalf("after death: err=%v first=%d second=%d loads=%d, want the call on a reloaded connector", err, first.calls, second.calls, loads)
	}
	if err := r.call(ctx, "m", nil, nil); err != nil || second.calls != 2 || loads != 1 {
		t.Fatalf("reloaded connector not reused: err=%v second=%d loads=%d", err, second.calls, loads)
	}
}

func TestReloaderFailedReloadFailsTheCallAndRetriesNext(t *testing.T) {
	dead := &fakeConn{dead: errors.New("trapped")}
	boom := errors.New("read module")
	live := &fakeConn{}
	var next error = boom
	r := &reloader{cur: dead, load: func() (conn, error) {
		if next != nil {
			return nil, next
		}
		return live, nil
	}}

	if err := r.call(context.Background(), "m", nil, nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the load error", err)
	}
	next = nil
	if err := r.call(context.Background(), "m", nil, nil); err != nil || live.calls != 1 {
		t.Fatalf("second call: err=%v live=%d, want a successful reload", err, live.calls)
	}
}

func TestReloaderBacksOffAfterRepeatedLoadFailures(t *testing.T) {
	now := time.Unix(1000, 0)
	defer func(c func() time.Time) { loadClock = c }(loadClock)
	loadClock = func() time.Time { return now }

	boom := errors.New("compile module")
	loads := 0
	var fail error = boom
	live := &fakeConn{}
	r := &reloader{cur: &fakeConn{dead: errors.New("trapped")}, load: func() (conn, error) {
		loads++
		if fail != nil {
			return nil, fail
		}
		return live, nil
	}}
	ctx := context.Background()

	for i := 0; i < maxLoadFailures; i++ {
		if err := r.call(ctx, "m", nil, nil); !errors.Is(err, boom) {
			t.Fatalf("call %d: err = %v, want the load error", i, err)
		}
	}
	err := r.call(ctx, "m", nil, nil)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "3 times in a row") || loads != maxLoadFailures {
		t.Fatalf("in cooldown: err=%v loads=%d, want the wrapped error and no load", err, loads)
	}

	now = now.Add(loadRetryAfter)
	if err := r.call(ctx, "m", nil, nil); !errors.Is(err, boom) || loads != maxLoadFailures+1 {
		t.Fatalf("after cooldown: err=%v loads=%d, want one more load", err, loads)
	}
	if err := r.call(ctx, "m", nil, nil); !strings.Contains(err.Error(), "4 times in a row") || loads != maxLoadFailures+1 {
		t.Fatalf("failed retry restarts the cooldown: err=%v loads=%d", err, loads)
	}

	now = now.Add(loadRetryAfter)
	fail = nil
	if err := r.call(ctx, "m", nil, nil); err != nil || live.calls != 1 {
		t.Fatalf("recovery: err=%v live=%d", err, live.calls)
	}
	live.dead = errors.New("trapped again")
	fail = boom
	for i := 0; i < maxLoadFailures; i++ {
		_ = r.call(ctx, "m", nil, nil)
	}
	if loads != maxLoadFailures+1+1+maxLoadFailures {
		t.Fatalf("success did not reset the count: loads=%d", loads)
	}
}

type failingConn struct{ err error }

func (f failingConn) Call(context.Context, string, any, any) error { return f.err }
func (f failingConn) Err() error                                   { return nil }

func TestReloaderNotesAFailedCallAndAFailedReload(t *testing.T) {
	type noted struct {
		method string
		err    error
	}
	var got []noted
	note := func(method string, err error) { got = append(got, noted{method, err}) }
	callErr, loadErr := errors.New("route refused"), errors.New("read module")

	r := &reloader{cur: failingConn{callErr}, note: note}
	if err := r.call(context.Background(), "tracker.get", nil, nil); !errors.Is(err, callErr) {
		t.Fatalf("err = %v, want the call error", err)
	}
	r = &reloader{cur: &fakeConn{dead: errors.New("trapped")}, note: note,
		load: func() (conn, error) { return nil, loadErr }}
	if err := r.call(context.Background(), "tracker.list", nil, nil); !errors.Is(err, loadErr) {
		t.Fatalf("err = %v, want the load error", err)
	}
	r = &reloader{cur: &fakeConn{}, note: note}
	if err := r.call(context.Background(), "tracker.ok", nil, nil); err != nil {
		t.Fatal(err)
	}

	want := []noted{{"tracker.get", callErr}, {"tracker.list", loadErr}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("noted = %v, want %v", got, want)
	}
}

// scriptedConn answers each call from errs in order, then succeeds. Err reports
// deadOnFail once a call has failed, as a real Connector closes itself.
type scriptedConn struct {
	errs             []error
	calls            int
	deadOnFail, dead error
}

func (s *scriptedConn) Call(context.Context, string, any, any) error {
	s.calls++
	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		s.dead = s.deadOnFail
		return err
	}
	return nil
}
func (s *scriptedConn) Err() error { return s.dead }

// A Connector closes itself on a failed call, so the retry only works when the
// wrap sits outside the reloader: the second attempt then passes the
// dead-Connector check and reloads.
func TestReloadingTrackerRetriesAReadOnTheReloadedConnector(t *testing.T) {
	transient := &connectorhost.CallError{Class: connectorhost.Transient, Message: "upstream 503"}
	first := &scriptedConn{errs: []error{transient}, deadOnFail: errors.New("closed after a failed call")}
	second := &scriptedConn{}
	loads := 0
	var noted []error
	r := &reloader{cur: first, load: func() (conn, error) { loads++; return second, nil },
		note: func(_ string, err error) { noted = append(noted, err) }}
	clock, slept := fakeClock()
	c := &connectorhost.Connector{Capabilities: []string{"tracker", CapComments}}
	tr, err := newFrom(c, r.call, testLifecycle, testTarget, clock)
	if err != nil {
		t.Fatal(err)
	}

	cl, ok := forge.As[forge.CommentLister](tr)
	if !ok {
		t.Fatal("tracker lacks CommentLister")
	}
	if _, err := cl.Comments("1"); err != nil {
		t.Fatalf("Comments: %v", err)
	}
	if first.calls != 1 || second.calls != 1 || loads != 1 {
		t.Errorf("first=%d second=%d loads=%d, want the retry on one reloaded connector", first.calls, second.calls, loads)
	}
	if len(*slept) != 1 {
		t.Errorf("slept %v, want one backoff", *slept)
	}
	if len(noted) != 1 || !errors.Is(noted[0], transient) {
		t.Errorf("noted = %v, want the first failure", noted)
	}
}
