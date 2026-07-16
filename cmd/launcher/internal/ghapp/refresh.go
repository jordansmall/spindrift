package ghapp

import (
	"context"
	"time"
)

// mintFunc mints a token and reports when it expires. Minter.Mint satisfies
// it; tests substitute a fake.
type mintFunc func(ctx context.Context) (token string, expiresAt time.Time, err error)

// Refresher keeps a published token fresh: it schedules a re-mint skew before
// each token's expiry and republishes the new token, for the life of a ctx.
type Refresher struct {
	mint    mintFunc
	publish func(token string)
	// skew is how long before expiry to re-mint, absorbing clock drift and
	// the mint round-trip so no gh call ever races an expiring token.
	skew time.Duration
	// retryDelay is the wait before retrying a failed re-mint. A live token
	// is still published, so retries only need to beat its expiry.
	retryDelay time.Duration
	now        func() time.Time
	after      func(time.Duration) <-chan time.Time
	log        func(format string, args ...any)
}

const (
	defaultSkew       = 10 * time.Minute
	defaultRetryDelay = 30 * time.Second
	// minWait floors the scheduled wait so a token minted with a very short
	// (or already-past) lifetime cannot spin the loop.
	minWait = 1 * time.Second
)

// run drives the refresh loop starting from firstExpiry (the expiry of the
// token already published by Start). It sleeps until skew before each expiry,
// re-mints, republishes, and repeats until ctx is cancelled. Extracted from
// Start so it is exercisable with injected clock, timer, and mint.
func (r *Refresher) run(ctx context.Context, firstExpiry time.Time) {
	expiry := firstExpiry
	for {
		wait := expiry.Sub(r.now()) - r.skew
		if wait < minWait {
			wait = minWait
		}
		if !r.sleep(ctx, wait) {
			return
		}
		token, newExpiry, ok := r.remint(ctx)
		if !ok {
			return // ctx cancelled while retrying
		}
		r.publish(token)
		expiry = newExpiry
	}
}

// remint mints one token, retrying after retryDelay until it succeeds or ctx
// is cancelled. The previously published token stays valid until skew before
// its expiry, so retries only need to win before then.
func (r *Refresher) remint(ctx context.Context) (string, time.Time, bool) {
	for {
		token, expiry, err := r.mint(ctx)
		if err == nil {
			return token, expiry, true
		}
		r.log("ghapp: token refresh failed, retrying in %s: %v", r.retryDelay, err)
		if !r.sleep(ctx, r.retryDelay) {
			return "", time.Time{}, false
		}
	}
}

// sleep waits d or reports false if ctx is cancelled first.
func (r *Refresher) sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-r.after(d):
		return true
	}
}

// Start mints an initial token synchronously, publishes it, and launches a
// background goroutine that re-mints before each expiry until ctx is
// cancelled. It returns the initial mint's error without starting the loop, so
// a misconfigured App degrades to whatever token is already ambient rather
// than aborting the run. On success the caller must cancel ctx to stop the
// goroutine.
func Start(ctx context.Context, m *Minter, publish func(string), log func(string, ...any)) error {
	r := &Refresher{
		mint:       m.Mint,
		publish:    publish,
		skew:       defaultSkew,
		retryDelay: defaultRetryDelay,
		now:        time.Now,
		after:      time.After,
		log:        log,
	}
	token, expiry, err := r.mint(ctx)
	if err != nil {
		return err
	}
	r.publish(token)
	go r.run(ctx, expiry)
	return nil
}
