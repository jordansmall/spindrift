package connectorhost

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type stubHost struct {
	calls int
	delay time.Duration
	body  []byte
}

func (s *stubHost) httpRequest(ctx context.Context, raw []byte, bufCap int) (int, []byte, http.Header, error) {
	s.calls++
	time.Sleep(s.delay)
	return 200, s.body, nil, nil
}

func (s *stubHost) refuse(call, why string) error { return errors.New(why) }

func TestBudgetExpiresWithErrBudget(t *testing.T) {
	ctx, b := newBudget(context.Background(), 30*time.Millisecond)
	defer b.close()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("budget never expired")
	}
	if !errors.Is(context.Cause(ctx), ErrBudget) {
		t.Fatalf("cause = %v, want ErrBudget", context.Cause(ctx))
	}
}

func TestBudgetPausedTimeDoesNotCount(t *testing.T) {
	ctx, b := newBudget(context.Background(), 60*time.Millisecond)
	defer b.close()
	host := &stubHost{delay: 150 * time.Millisecond}
	if _, _, _, err := b.httpRequest(ctx, host, nil, 0); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatalf("a host call longer than the budget spent it: %v", context.Cause(ctx))
	}
	select {
	case <-ctx.Done():
		if !errors.Is(context.Cause(ctx), ErrBudget) {
			t.Fatalf("cause = %v", context.Cause(ctx))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("budget did not resume")
	}
}

func TestBudgetResponseCreditExtends(t *testing.T) {
	ctx, b := newBudget(context.Background(), 40*time.Millisecond)
	defer b.close()
	// 1 MiB of response earns budgetPerMiB, far beyond the test's window.
	host := &stubHost{body: make([]byte, 1<<20)}
	if _, _, _, err := b.httpRequest(ctx, host, nil, 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatalf("credited budget expired: %v", context.Cause(ctx))
	}
}

func TestBudgetCreditIsCapped(t *testing.T) {
	_, b := newBudget(context.Background(), time.Second)
	defer b.close()
	var total time.Duration
	for range 3 {
		total += b.credit(maxResponseCredit)
	}
	want := time.Duration(maxResponseCredit) * budgetPerMiB >> 20
	if total != want {
		t.Fatalf("credited %v over three full responses, want %v", total, want)
	}
}

func TestBudgetRefusesTheRequestPastTheLimit(t *testing.T) {
	ctx, b := newBudget(context.Background(), time.Minute)
	defer b.close()
	host := &stubHost{}
	for i := range maxHTTPRequests {
		if _, _, _, err := b.httpRequest(ctx, host, nil, 0); err != nil {
			t.Fatalf("request %d refused: %v", i+1, err)
		}
	}
	_, _, _, err := b.httpRequest(ctx, host, nil, 0)
	if !errors.Is(err, ErrHTTPRequestLimit) {
		t.Fatalf("request %d: err = %v, want ErrHTTPRequestLimit", maxHTTPRequests+1, err)
	}
	if host.calls != maxHTTPRequests {
		t.Fatalf("host served %d requests, want %d", host.calls, maxHTTPRequests)
	}
}

func TestBudgetExpiredBeforePauseStaysExpired(t *testing.T) {
	ctx, b := newBudget(context.Background(), 10*time.Millisecond)
	defer b.close()
	time.Sleep(60 * time.Millisecond)
	host := &stubHost{body: make([]byte, 1<<20)}
	if _, _, _, err := b.httpRequest(ctx, host, nil, 0); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(context.Cause(ctx), ErrBudget) {
		t.Fatalf("cause = %v, want ErrBudget", context.Cause(ctx))
	}
	time.Sleep(20 * time.Millisecond)
	if !errors.Is(context.Cause(ctx), ErrBudget) {
		t.Fatalf("an expired budget was revived: %v", context.Cause(ctx))
	}
}
