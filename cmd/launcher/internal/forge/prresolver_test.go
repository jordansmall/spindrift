package forge_test

import (
	"fmt"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
)

// recordingClock is a fake retry.Clock that records durations passed to
// Sleep instead of actually sleeping, mirroring
// internal/retry/retry_test.go's recordingClock.
type recordingClock struct {
	sleeps []time.Duration
}

func (r *recordingClock) Clock() retry.Clock {
	return retry.Clock{
		Now: time.Now,
		Sleep: func(d time.Duration) {
			r.sleeps = append(r.sleeps, d)
		},
	}
}

// TestResolveOpenPR verifies forge.ResolveOpenPR's single documented absent
// policy: a push-only Code Forge (no PRForge) and "no open PR yet" both
// resolve to Found: false with no error; a found PR reports its URL.
func TestResolveOpenPR(t *testing.T) {
	t.Run("push-only forge has no PR to discover", func(t *testing.T) {
		f := forge.NewFake()
		f.BranchPrefix = "agent/issue-"
		f.SetPR("agent/issue-42", forge.PR{URL: "https://github.com/o/r/pull/7"})

		res, err := forge.ResolveOpenPR(f.AsPushOnly(), "42")
		if err != nil || res.Found {
			t.Fatalf("want {Found:false} nil; got %+v, err=%v", res, err)
		}
	})

	t.Run("no open PR yet", func(t *testing.T) {
		f := forge.NewFake()
		f.BranchPrefix = "agent/issue-"

		res, err := forge.ResolveOpenPR(f, "42")
		if err != nil || res.Found {
			t.Fatalf("want {Found:false} nil; got %+v, err=%v", res, err)
		}
	})

	t.Run("open PR found reports URL", func(t *testing.T) {
		f := forge.NewFake()
		f.BranchPrefix = "agent/issue-"
		f.SetPR("agent/issue-42", forge.PR{URL: "https://github.com/o/r/pull/7"})

		res, err := forge.ResolveOpenPR(f, "42")
		if err != nil || !res.Found || res.URL != "https://github.com/o/r/pull/7" {
			t.Fatalf("want {Found:true URL:.../pull/7} nil; got %+v, err=%v", res, err)
		}
	})
}

// TestResolveOpenPRFiles verifies ResolveOpenPRFiles absorbs the PRForge
// assertion and ListPRFiles call so callers don't need their own assertion
// after resolving: push-only and no-open-PR both yield (nil, nil), a found
// PR's changed files are returned, and a ListPRFiles failure propagates.
func TestResolveOpenPRFiles(t *testing.T) {
	t.Run("push-only forge has no PR to discover", func(t *testing.T) {
		f := forge.NewFake()
		f.BranchPrefix = "agent/issue-"

		got, err := forge.ResolveOpenPRFiles(f.AsPushOnly(), "42")
		if err != nil || got != nil {
			t.Fatalf("want nil, nil; got %v, err=%v", got, err)
		}
	})

	t.Run("no open PR yet", func(t *testing.T) {
		f := forge.NewFake()
		f.BranchPrefix = "agent/issue-"

		got, err := forge.ResolveOpenPRFiles(f, "42")
		if err != nil || got != nil {
			t.Fatalf("want nil, nil; got %v, err=%v", got, err)
		}
	})

	t.Run("open PR found returns its changed files", func(t *testing.T) {
		f := forge.NewFake()
		f.BranchPrefix = "agent/issue-"
		f.SetPR("agent/issue-42", forge.PR{URL: "https://github.com/o/r/pull/7"})
		f.SetPRFiles("https://github.com/o/r/pull/7", []string{"a.go", "b.go"})

		got, err := forge.ResolveOpenPRFiles(f, "42")
		if err != nil || len(got) != 2 || got[0] != "a.go" || got[1] != "b.go" {
			t.Fatalf("want [a.go b.go], nil; got %v, err=%v", got, err)
		}
	})

	t.Run("ListPRFiles failure propagates", func(t *testing.T) {
		f := forge.NewFake()
		f.BranchPrefix = "agent/issue-"
		f.SetPR("agent/issue-42", forge.PR{URL: "https://github.com/o/r/pull/7"})
		f.PRFilesErr = fmt.Errorf("boom")

		got, err := forge.ResolveOpenPRFiles(f, "42")
		if err == nil || got != nil {
			t.Fatalf("want nil, error; got %v, err=%v", got, err)
		}
	})
}

// TestResolveOpenPRWithRetry verifies ResolveOpenPRWithRetry retries only on
// a transient lookup error, backing off between attempts via the injected
// retry.LinearBackoff, and never retries a definitive "no open PR" result or
// a non-transient error (issue #2323).
func TestResolveOpenPRWithRetry(t *testing.T) {
	t.Run("transient error then success adopts the PR", func(t *testing.T) {
		f := forge.NewFake()
		f.BranchPrefix = "agent/issue-"
		f.SetPR("agent/issue-42", forge.PR{URL: "https://github.com/o/r/pull/7"})
		f.OpenPRForBranchErrs = []error{
			fmt.Errorf("gh pr list: exit status 1: HTTP 502: Bad Gateway"),
			fmt.Errorf("gh pr list: exit status 1: HTTP 503: Service Unavailable"),
		}

		rc := &recordingClock{}
		backoff := retry.LinearBackoff{Unit: time.Second, Clock: rc.Clock()}

		res, err := forge.ResolveOpenPRWithRetry(f, "42", backoff, 5)
		if err != nil {
			t.Fatalf("want nil error; got %v", err)
		}
		if !res.Found || res.URL != "https://github.com/o/r/pull/7" {
			t.Fatalf("want {Found:true URL:.../pull/7}; got %+v", res)
		}
		if len(rc.sleeps) != 2 {
			t.Fatalf("want 2 sleeps (backoff before attempts 2 and 3); got %v", rc.sleeps)
		}
	})

	t.Run("persistent transient error exhausts attempts and returns the error", func(t *testing.T) {
		f := forge.NewFake()
		f.BranchPrefix = "agent/issue-"
		f.OpenPRForBranchErr = fmt.Errorf("gh pr list: exit status 1: connection reset")

		rc := &recordingClock{}
		backoff := retry.LinearBackoff{Unit: time.Second, Clock: rc.Clock()}

		const maxAttempts = 4
		res, err := forge.ResolveOpenPRWithRetry(f, "42", backoff, maxAttempts)
		if err == nil {
			t.Fatalf("want non-nil error; got nil (res=%+v)", res)
		}
		if len(rc.sleeps) != maxAttempts-1 {
			t.Fatalf("want %d sleeps; got %d (%v)", maxAttempts-1, len(rc.sleeps), rc.sleeps)
		}
	})

	t.Run("no open PR yet does not retry", func(t *testing.T) {
		f := forge.NewFake()
		f.BranchPrefix = "agent/issue-"

		rc := &recordingClock{}
		backoff := retry.LinearBackoff{Unit: time.Second, Clock: rc.Clock()}

		res, err := forge.ResolveOpenPRWithRetry(f, "42", backoff, 5)
		if err != nil || res.Found {
			t.Fatalf("want {Found:false} nil; got %+v, err=%v", res, err)
		}
		if len(rc.sleeps) != 0 {
			t.Fatalf("want 0 sleeps; got %v", rc.sleeps)
		}
	})

	t.Run("non-transient error does not retry", func(t *testing.T) {
		f := forge.NewFake()
		f.BranchPrefix = "agent/issue-"
		f.OpenPRForBranchErr = fmt.Errorf("gh pr list: exit status 1: no pull requests found")

		rc := &recordingClock{}
		backoff := retry.LinearBackoff{Unit: time.Second, Clock: rc.Clock()}

		res, err := forge.ResolveOpenPRWithRetry(f, "42", backoff, 5)
		if err == nil {
			t.Fatalf("want non-nil error; got nil (res=%+v)", res)
		}
		if len(rc.sleeps) != 0 {
			t.Fatalf("want 0 sleeps; got %v", rc.sleeps)
		}
	})
}
