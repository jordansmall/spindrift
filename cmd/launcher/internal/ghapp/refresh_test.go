package ghapp

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestRefresherRepublishesBeforeExpiry drives the loop with a manual timer and
// a scripted mint, asserting each scheduled tick re-mints and republishes the
// next token — the refresh mechanism the ticket requires be exercised, not
// merely asserted.
func TestRefresherRepublishesBeforeExpiry(t *testing.T) {
	base := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)

	ticks := make(chan time.Time)
	var mu sync.Mutex
	var published []string
	var waits []time.Duration

	mintCalls := 0
	r := &Refresher{
		mint: func(ctx context.Context) (string, time.Time, error) {
			mintCalls++
			return fmt.Sprintf("tok-%d", mintCalls), base.Add(time.Duration(mintCalls) * time.Hour), nil
		},
		publish: func(tok string) {
			mu.Lock()
			published = append(published, tok)
			mu.Unlock()
		},
		skew:       10 * time.Minute,
		retryDelay: 30 * time.Second,
		now:        func() time.Time { return base },
		after: func(d time.Duration) <-chan time.Time {
			mu.Lock()
			waits = append(waits, d)
			mu.Unlock()
			return ticks
		},
		log: func(string, ...any) {},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// firstExpiry one hour out; the loop should wait ~50m (expiry - skew).
	done := make(chan struct{})
	go func() { r.run(ctx, base.Add(1*time.Hour)); close(done) }()

	// Two scheduled re-mints.
	ticks <- base
	ticks <- base
	// Let the second mint's publish land, then stop the loop: cancel unblocks
	// the pending after() select via ctx.Done, so no further tick is needed.
	waitForLen(t, &mu, &published, 2)
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(published) != 2 {
		t.Fatalf("published = %v, want 2 tokens", published)
	}
	if published[0] != "tok-1" || published[1] != "tok-2" {
		t.Errorf("published = %v, want [tok-1 tok-2]", published)
	}
	if len(waits) == 0 || waits[0] != 50*time.Minute {
		t.Errorf("first wait = %v, want 50m (expiry - skew)", waits)
	}
}

// TestRefresherRetriesOnMintError confirms a failed re-mint retries after
// retryDelay rather than giving up (the live token stays published).
func TestRefresherRetriesOnMintError(t *testing.T) {
	base := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	ticks := make(chan time.Time)
	var mu sync.Mutex
	var published []string
	var waits []time.Duration

	calls := 0
	r := &Refresher{
		mint: func(ctx context.Context) (string, time.Time, error) {
			calls++
			if calls == 1 {
				return "", time.Time{}, fmt.Errorf("boom")
			}
			return "recovered", base.Add(time.Hour), nil
		},
		publish: func(tok string) { mu.Lock(); published = append(published, tok); mu.Unlock() },
		skew:    10 * time.Minute, retryDelay: 30 * time.Second,
		now: func() time.Time { return base },
		after: func(d time.Duration) <-chan time.Time {
			mu.Lock()
			waits = append(waits, d)
			mu.Unlock()
			return ticks
		},
		log: func(string, ...any) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { r.run(ctx, base.Add(time.Hour)); close(done) }()

	ticks <- base // first scheduled tick -> mint fails -> schedules retryDelay
	ticks <- base // retry tick -> mint succeeds -> publishes
	waitForLen(t, &mu, &published, 1)
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(published) != 1 || published[0] != "recovered" {
		t.Fatalf("published = %v, want [recovered]", published)
	}
	// waits: [50m schedule, 30s retry, 50m next schedule].
	if len(waits) < 2 || waits[1] != 30*time.Second {
		t.Errorf("second wait = %v, want a 30s retry", waits)
	}
}

func waitForLen(t *testing.T, mu *sync.Mutex, s *[]string, n int) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		mu.Lock()
		got := len(*s)
		mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d publishes", n)
}
