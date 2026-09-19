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

// Issue #1364 slice 5, AC4: a --continuous-dispatch wave stops for rebuild via
// the ErrImageStale / exit-4 path when the launcher is stale but the image is
// fresh. This test builds the FreshnessChecker from the real freshness.Probe
// rather than a hand-rolled stale closure, so it covers the Probe to
// FreshnessChecker to RunContinuous plumbing itself.
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
		res := freshness.Probe(freshness.ProbeSpec{
			RunnerKind:         "podman",
			Pwd:                pwd,
			BaseBranch:         "main",
			FlakeImageAttr:     imageAttr,
			ImageTag:           "spindrift:" + testutil.SameHash,
			FlakeLauncherAttr:  launcherAttr,
			LoadedLauncherHash: testutil.SameHash,
		}, eval)
		lastMessage = res.Message
		return res.Applicable, res.Fresh, res.Message
	}

	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	// The probe is stale from the very first refill call, so no Box ever
	// launches at all. That is the strongest form of "no further Box launched".
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
	go func() { resultCh <- RunContinuous(c, nil, fc, fc, f, s, QueueFromDiscoverer(discover), fresh) }()

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
