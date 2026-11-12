package settle

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/seambundle"
)

func newRetrySettle(clock dispatch.Clock) *Settle {
	c := baseConfig()
	c.Policy = retry.Policy{Max: 3, Unit: time.Second}
	c.Clock = clock
	return newTestSettle(c, forge.NewFake(), forge.NewFake())
}

// failThenSucceed scripts n relay failures followed by success.
func failThenSucceed(n int) []error {
	errs := make([]error, n)
	for i := range errs {
		errs[i] = errors.New("ssh auth blip")
	}
	return append(errs, nil)
}

// stopOnRelayBackoff builds a clock whose sleep marks num terminated once the
// relay has been attempted, so the stop lands inside the relay's backoff and
// never in an earlier gate or confirm sleep. It records every sleep.
func stopOnRelayBackoff(sp **Settle, fc *forge.Fake, num string) (*[]time.Duration, dispatch.Clock) {
	var sleeps []time.Duration
	return &sleeps, dispatch.Clock{Now: time.Now, Sleep: func(d time.Duration) {
		sleeps = append(sleeps, d)
		if len(fc.RelayBundleCalls) > 0 {
			(*sp).term.Mark(num)
		}
	}}
}

// A nil inner is the only input here Settle's capability shapes never produce:
// every shape that reaches a relay or draft-PR create supplies one.
func TestInternalSeam_RetryingRelay_NilInnerStaysNil(t *testing.T) {
	_, clock := recordingClock()
	if got := newRetrySettle(clock).retryingRelay("1", 0, nil); got != nil {
		t.Errorf("retryingRelay(nil) = %v, want nil", got)
	}
}

// Same, for the draft-PR creator.
func TestInternalSeam_RetryingDraftPR_NilInnerStaysNil(t *testing.T) {
	s := newRetrySettle(dispatch.Clock{})
	if got := s.retryingDraftPR("7", 0, nil, nil, nil); got != nil {
		t.Errorf("retryingDraftPR(nil) = %v, want nil", got)
	}
}

// A real relay that fails every attempt after `git bundle verify` passes and
// the fetch starts must give up after the bound and leave the Box's bundle in
// the outbox untouched, so a human can re-run the relay by hand. Settle's
// tables drive the Fake relay; only this seam runs the real git adapter.
func TestInternalSeam_RetryingRelay_RealRelayFailingEveryAttemptLeavesBundleUntouched(t *testing.T) {
	_, clock := recordingClock()
	const branch = "agent/issue-7"
	// The seeding commits run git directly, with no identity of their own.
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")
	seed := forgetest.NewGitRepoFixture(t, "main")
	outbox := t.TempDir()
	forgetest.SeedRelayBundle(t, seed.Bare, "main", outbox, branch)
	path := filepath.Join(outbox, seambundle.FileName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A stale ref lock makes the fetch's ref update fail on every attempt, while
	// the bundle still verifies against the repo's real history.
	lock := filepath.Join(seed.Bare, "refs", "heads", branch+".lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	parent := local.ResolveParent("7", "")
	inner := local.NewLocalCodeForge(seed.Bare, "main", parent, "Test Bot", "bot@example.com").(forge.BundleRelay)
	rec := &ProgressRecorder{}
	r := &retryingBundleRelay{
		retryLoop: retryLoop{
			backoff:    retry.LinearBackoff{Unit: time.Second, Clock: clock},
			maxRetries: 3,
			stopped:    func() bool { return false },
			num:        "7",
			progress:   rec,
		},
		inner: inner,
	}

	relayErr := r.RelayBundle(outbox, branch)

	if relayErr == nil || errors.Is(relayErr, forge.ErrBundleNotFound) {
		t.Fatalf("err = %v, want a persistent non-bundle-missing relay failure", relayErr)
	}
	if msg := relayErr.Error(); strings.Contains(msg, "malformed bundle") || !strings.Contains(msg, "fetch bundle") {
		t.Errorf("err = %v, want a failure in the fetch step after the bundle verified", relayErr)
	}
	retries := 0
	for _, st := range rec.Steps() {
		if st.Status == StatusRelayRetry {
			retries++
		}
	}
	if retries != 3 {
		t.Errorf("got %d relay-retry steps, want 3: %+v", retries, rec.Steps())
	}
	after, rerr := os.ReadFile(path)
	if rerr != nil || !bytes.Equal(after, before) {
		t.Errorf("outbox bundle changed across retries: %d bytes -> %d bytes, %v", len(before), len(after), rerr)
	}
}
