package waves

import (
	"errors"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/testutil"
)

// TestRunContinuous_RealProbe_LauncherStaleImageFresh is issue #1364 slice
// 5's AC4 coverage: a --continuous-dispatch wave stops for rebuild via the
// existing ErrImageStale / exit-4 path when the launcher is stale but the
// image is fresh. Unlike TestRunContinuous_StaleProbeStopsRefillLetsInFlightFinish,
// which wires RunContinuous to a synthetic closure that hand-returns
// (applicable, fresh, message) tuples, this test builds the FreshnessChecker
// by calling the REAL freshness.Probe — wired to a freshness.Fake evaluator
// whose image-attr outpath matches the loaded image tag (image fresh) but
// whose launcher-attr outpath differs from the loaded launcher hash
// (launcher stale) — and hands that closure to RunContinuous. This proves
// the Probe -> FreshnessChecker -> RunContinuous plumbing is wired for real,
// not just that RunContinuous reacts correctly to a hand-rolled stale
// signal (already covered above).
func TestRunContinuous_RealProbe_LauncherStaleImageFresh(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")

	const (
		imageAttr    = ".#packages.x86_64-linux.agent-image"
		launcherAttr = ".#packages.x86_64-linux.launcher"
	)
	eval := &freshness.Fake{
		OutPathForAttr: map[string]string{
			"packages.x86_64-linux.agent-image": "/nix/store/" + testutil.SameHash + "-agent-image",
			"packages.x86_64-linux.launcher":    "/nix/store/" + testutil.DiffHash + "-launcher",
		},
	}
	var lastMessage string
	fresh := func() (bool, bool, string) {
		res := freshness.Probe("podman", pwd, "main", imageAttr, "spindrift:"+testutil.SameHash, launcherAttr, testutil.SameHash, eval)
		lastMessage = res.Message
		return res.Applicable, res.Fresh, res.Message
	}

	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})

	// The probe is stale from the very first refill call (this fixture never
	// flips fresh->stale mid-run the way
	// TestRunContinuous_StaleProbeStopsRefillLetsInFlightFinish's synthetic
	// closure does), so no Box ever launches at all — the strongest form of
	// "no further Box launched".
	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	discover := func() (Batch, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return Batch{}, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return Batch{Issues: out, Edges: map[string][]string{}}, nil
	}

	resultCh := make(chan error, 1)
	go func() { resultCh <- RunContinuous(c, nil, fc, fc, dir, f, s, discover, fresh) }()

	var runErr error
	select {
	case runErr = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return")
	}

	if !errors.Is(runErr, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", runErr)
	}

	if len(fr.RunCalls) != 0 {
		t.Fatalf("RunCalls: got %v, want none (the real probe reports stale on the very first refill, before any Box launches)", fr.RunCalls)
	}

	if want := "launcher"; !strings.Contains(lastMessage, want) {
		t.Errorf("wave-surfaced message %q does not name %q as the stale dimension", lastMessage, want)
	}
}
