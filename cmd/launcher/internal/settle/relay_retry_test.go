package settle

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
)

// scriptedRelay fails its first failures calls with a distinct error each, then
// succeeds; with failErr set it returns that on every call instead.
type scriptedRelay struct {
	failures int
	failErr  error
	calls    int
	dirs     []string
	errs     []error
}

func (r *scriptedRelay) RelayBundle(outboxDir, ref string) error {
	r.calls++
	r.dirs = append(r.dirs, outboxDir)
	var err error
	switch {
	case r.failErr != nil:
		err = r.failErr
	case r.calls <= r.failures:
		err = fmt.Errorf("relay failure %d", r.calls)
	}
	r.errs = append(r.errs, err)
	return err
}

func newRetrySettle(clock dispatch.Clock) *Settle {
	c := baseConfig()
	c.Policy = retry.Policy{Max: 3, Unit: time.Second}
	c.Clock = clock
	return newTestSettle(c, forge.NewFake(), forge.NewFake())
}

func relayRetryLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "status=relay-retry") {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestRetryingRelay_NilInnerStaysNil(t *testing.T) {
	_, clock := recordingClock()
	if got := newRetrySettle(clock).retryingRelay("1", 0, nil); got != nil {
		t.Errorf("retryingRelay(nil) = %v, want nil", got)
	}
}

func TestRetryingRelay_RecoversAfterFailures(t *testing.T) {
	sleeps, clock := recordingClock()
	s := newRetrySettle(clock)
	inner := &scriptedRelay{failures: 2}

	var err error
	out := captureStdout(t, func() {
		err = s.retryingRelay("7", 0, inner).RelayBundle(t.TempDir(), "agent/issue-7")
	})

	if err != nil {
		t.Fatalf("RelayBundle: unexpected error: %v", err)
	}
	if inner.calls != 3 {
		t.Errorf("inner called %d times, want 3", inner.calls)
	}
	lines := relayRetryLines(out)
	if len(lines) != 2 {
		t.Fatalf("got %d retry lines, want 2:\n%s", len(lines), out)
	}
	for i, l := range lines {
		want := fmt.Sprintf("attempt=%d/3", i+1)
		if !strings.Contains(l, want) || !strings.Contains(l, "#7") || !strings.Contains(l, "agent/issue-7") {
			t.Errorf("line %d = %q, want it to carry #7, the ref, and %s", i, l, want)
		}
	}
	if len(*sleeps) == 0 {
		t.Error("no backoff sleeps recorded between retries")
	}
}

func TestRetryingRelay_ExhaustsAndReturnsLastError(t *testing.T) {
	_, clock := recordingClock()
	s := newRetrySettle(clock)
	inner := &scriptedRelay{failures: 100}
	outbox := t.TempDir()
	bundle := []byte("bundle-bytes")
	path := filepath.Join(outbox, "bundle")
	if err := os.WriteFile(path, bundle, 0o644); err != nil {
		t.Fatal(err)
	}

	var err error
	out := captureStdout(t, func() {
		err = s.retryingRelay("7", 0, inner).RelayBundle(outbox, "agent/issue-7")
	})

	if inner.calls != 4 {
		t.Errorf("inner called %d times, want 4 (1 + Max retries)", inner.calls)
	}
	if want := inner.errs[len(inner.errs)-1]; !errors.Is(err, want) {
		t.Errorf("err = %v, want the last inner error %v", err, want)
	}
	if n := len(relayRetryLines(out)); n != 3 {
		t.Errorf("got %d retry lines, want 3:\n%s", n, out)
	}
	got, rerr := os.ReadFile(path)
	if rerr != nil || !bytes.Equal(got, bundle) {
		t.Errorf("outbox bundle changed across retries: %q, %v", got, rerr)
	}
}

func TestRetryingRelay_ZeroMaxIsSingleAttempt(t *testing.T) {
	sleeps, clock := recordingClock()
	s := newRetrySettle(clock)
	s.cfg.Policy.Max = 0
	inner := &scriptedRelay{failures: 100}

	err := s.retryingRelay("7", 0, inner).RelayBundle(t.TempDir(), "r")

	if err == nil || inner.calls != 1 || len(*sleeps) != 0 {
		t.Errorf("err=%v calls=%d sleeps=%v, want an error after exactly 1 call and no sleeps", err, inner.calls, *sleeps)
	}
}

func TestRetryingRelay_BundleNotFoundIsNotRetried(t *testing.T) {
	sleeps, clock := recordingClock()
	s := newRetrySettle(clock)
	inner := &scriptedRelay{failErr: fmt.Errorf("outbox: %w", forge.ErrBundleNotFound)}

	var err error
	out := captureStdout(t, func() {
		err = s.retryingRelay("7", 0, inner).RelayBundle(t.TempDir(), "r")
	})

	if !errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("err = %v, want ErrBundleNotFound", err)
	}
	if inner.calls != 1 || len(*sleeps) != 0 || len(relayRetryLines(out)) != 0 {
		t.Errorf("calls=%d sleeps=%v retry lines=%d, want a single un-retried call", inner.calls, *sleeps, len(relayRetryLines(out)))
	}
}

func TestRetryingRelay_TerminationDuringBackoffAbandons(t *testing.T) {
	var s *Settle
	var gen uint64
	calls := 0
	clock := dispatch.Clock{Now: time.Now, Sleep: func(time.Duration) {
		calls++
		s.term.Mark("7")
	}}
	s = newRetrySettle(clock)
	gen = s.term.Begin("7")
	relayErr := errors.New("ssh auth blip")
	inner := &scriptedRelay{failErr: relayErr}

	var err error
	captureStdout(t, func() {
		err = s.retryingRelay("7", gen, inner).RelayBundle(t.TempDir(), "r")
	})

	if !errors.Is(err, errAbandoned) || !errors.Is(err, relayErr) {
		t.Errorf("err = %v, want it to wrap both errAbandoned and the relay error", err)
	}
	if inner.calls != 1 {
		t.Errorf("inner called %d times, want 1 (no relay after the stop)", inner.calls)
	}
	if calls != 1 {
		t.Errorf("slept %d slices after the mark, want 1", calls)
	}
}
