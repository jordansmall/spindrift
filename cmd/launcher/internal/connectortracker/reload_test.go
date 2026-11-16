package connectortracker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
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
