package main

import (
	"bytes"
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/waves"
)

// TestClassifyStaleOutcome_ContentDivergence_RecordsAndRebuilds verifies that
// a stale verdict with no prior recorded rev is treated as content staleness
// (a new base tip a rebuild will fix): it returns waves.ErrImageStale, writes
// nothing to out, and records staleRev for the next run to compare against.
func TestClassifyStaleOutcome_ContentDivergence_RecordsAndRebuilds(t *testing.T) {
	tracker := newStaleRevTracker(t.TempDir())
	var out bytes.Buffer
	diag := func() string { return "should not be called" }

	err := classifyStaleOutcome("revA", "spindrift:hash", tracker, diag, &out)

	if !errors.Is(err, waves.ErrImageStale) {
		t.Errorf("err = %v, want waves.ErrImageStale", err)
	}
	if out.Len() != 0 {
		t.Errorf("out = %q, want empty", out.String())
	}
	if got := tracker.prior(); got != "revA" {
		t.Errorf("tracker.prior() = %q, want %q", got, "revA")
	}
}

// TestClassifyStaleOutcome_NonConverging_DiagsAndHalts verifies that a stale
// verdict at the SAME rev as the prior recorded run — a rebuild already
// happened and it's still stale — is classified as host-tainted: it returns
// errImageHostTainted, writes the diagnostic to out, and clears the tracker.
func TestClassifyStaleOutcome_NonConverging_DiagsAndHalts(t *testing.T) {
	pwd := t.TempDir()
	tracker := newStaleRevTracker(pwd)
	if err := tracker.record("revA"); err != nil {
		t.Fatalf("tracker.record: %v", err)
	}
	var out bytes.Buffer
	const diagText = "sentinel diagnostic"
	diag := func() string { return diagText }

	err := classifyStaleOutcome("revA", "spindrift:hash", tracker, diag, &out)

	if !errors.Is(err, errImageHostTainted) {
		t.Errorf("err = %v, want errImageHostTainted", err)
	}
	if got := out.String(); got != diagText+"\n" {
		t.Errorf("out = %q, want %q", got, diagText+"\n")
	}
	if got := tracker.prior(); got != "" {
		t.Errorf("tracker.prior() = %q, want empty (cleared)", got)
	}
}

// TestClassifyStaleOutcome_DifferentRevAfterPrior_RecordsAndRebuilds verifies
// that a stale verdict at a DIFFERENT rev than the prior recorded run — a
// genuinely new base tip — is content staleness, not host taint: it returns
// waves.ErrImageStale and records the new rev.
func TestClassifyStaleOutcome_DifferentRevAfterPrior_RecordsAndRebuilds(t *testing.T) {
	pwd := t.TempDir()
	tracker := newStaleRevTracker(pwd)
	if err := tracker.record("revA"); err != nil {
		t.Fatalf("tracker.record: %v", err)
	}
	var out bytes.Buffer
	diag := func() string { return "should not be called" }

	err := classifyStaleOutcome("revB", "spindrift:hash", tracker, diag, &out)

	if !errors.Is(err, waves.ErrImageStale) {
		t.Errorf("err = %v, want waves.ErrImageStale", err)
	}
	if out.Len() != 0 {
		t.Errorf("out = %q, want empty", out.String())
	}
	if got := tracker.prior(); got != "revB" {
		t.Errorf("tracker.prior() = %q, want %q", got, "revB")
	}
}

// TestClassifyStaleOutcome_EmptyStaleRev_NeverHostTainted verifies that an
// empty staleRev (a transient fetch failure, not a resolved base-tip rev) is
// never classified as host-tainted, even with an empty prior — NonConverging
// treats "" as "unknown", not "same as before".
func TestClassifyStaleOutcome_EmptyStaleRev_NeverHostTainted(t *testing.T) {
	tracker := newStaleRevTracker(t.TempDir())
	var out bytes.Buffer
	diag := func() string { return "should not be called" }

	err := classifyStaleOutcome("", "spindrift:hash", tracker, diag, &out)

	if !errors.Is(err, waves.ErrImageStale) {
		t.Errorf("err = %v, want waves.ErrImageStale", err)
	}
	if out.Len() != 0 {
		t.Errorf("out = %q, want empty", out.String())
	}
}

// TestClassifyStaleOutcome_SameRevEmptyTipTag_RebuildsNotHostTaint verifies
// that a stale verdict at the SAME rev as the prior recorded run, but with
// an empty staleTipTag, is NOT classified as host-tainted: it's a stuck
// eval/tag-derivation failure repeating at the same rev, not a genuine
// host-taint divergence (which always has a derived tip tag). It returns
// waves.ErrImageStale, writes nothing to out, and records the rev so the
// loop keeps rebuilding and retrying.
func TestClassifyStaleOutcome_SameRevEmptyTipTag_RebuildsNotHostTaint(t *testing.T) {
	pwd := t.TempDir()
	tracker := newStaleRevTracker(pwd)
	if err := tracker.record("revA"); err != nil {
		t.Fatalf("tracker.record: %v", err)
	}
	var out bytes.Buffer
	diag := func() string { return "should not be called" }

	err := classifyStaleOutcome("revA", "", tracker, diag, &out)

	if !errors.Is(err, waves.ErrImageStale) {
		t.Errorf("err = %v, want waves.ErrImageStale", err)
	}
	if out.Len() != 0 {
		t.Errorf("out = %q, want empty", out.String())
	}
	if got := tracker.prior(); got != "revA" {
		t.Errorf("tracker.prior() = %q, want %q", got, "revA")
	}
}

// TestExitCodeFor verifies the full error-to-exit-code mapping, including
// the new exit 5 for errImageHostTainted (issue #2113) alongside the
// existing sentinels.
func TestExitCodeFor(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"errQueueEmpty", errQueueEmpty, 2},
		{"ErrOpenNoneDispatchable", waves.ErrOpenNoneDispatchable, 3},
		{"ErrImageStale", waves.ErrImageStale, 4},
		{"errImageHostTainted", errImageHostTainted, 5},
		{"other error", errors.New("boom"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCodeFor(tc.err); got != tc.want {
				t.Errorf("exitCodeFor(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}
