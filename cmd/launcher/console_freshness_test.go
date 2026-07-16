package main

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/freshness"
)

var errBoomFreshness = errors.New("pull failed")

// TestNewConsoleFreshnessChecker_RebuildThenCheck_ReportsFreshAtSameTip
// verifies the checker recognizes a tip it just rebuilt against as fresh —
// the fix for the static imageTag comparand baked at process start never
// updating in-process (issue #652). Without this, a successful rebuild
// would leave the very next freshness check reporting stale forever, since
// Probe's comparand (c.imageTag) can't be recomputed without a fresh
// process. probe is scripted directly (no real git/nix) since the
// checker's own caching logic, not freshness.Probe's plumbing, is under
// test here — Probe's own git/eval seam is exercised by internal/freshness's
// tests instead.
func TestNewConsoleFreshnessChecker_RebuildThenCheck_ReportsFreshAtSameTip(t *testing.T) {
	rev := "abc123"
	stale := freshness.Result{Applicable: true, Fresh: false, Rev: rev, Message: "rebuild needed"}
	probeCalls := 0
	probe := func() freshness.Result { probeCalls++; return stale }

	buildCalls := 0
	fresh, rebuild := newConsoleFreshnessChecker("main", probe, func() error { return nil }, func() (string, error) { buildCalls++; return "", nil })

	if applicable, isFresh, msg := fresh(); !applicable || isFresh {
		t.Fatalf("initial check: applicable=%v fresh=%v msg=%q, want stale", applicable, isFresh, msg)
	}

	if _, err := rebuild(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if buildCalls != 1 {
		t.Fatalf("buildCalls = %d, want 1", buildCalls)
	}
	if probeCalls != 2 {
		t.Fatalf("probeCalls = %d, want 2 (one for the initial check, one for rebuild's own re-probe)", probeCalls)
	}

	if applicable, isFresh, msg := fresh(); !applicable || !isFresh {
		t.Errorf("after rebuild: applicable=%v fresh=%v msg=%q, want fresh", applicable, isFresh, msg)
	}
}

// TestNewConsoleFreshnessChecker_OriginAdvancesAfterRebuild_StaleAgain
// verifies the rev-based fresh cache doesn't paper over a genuine second
// staleness: once the underlying probe reports a different rev than the one
// rebuild last rebuilt, the checker must report stale again rather than
// treating any prior rebuild as permanently sufficient.
func TestNewConsoleFreshnessChecker_OriginAdvancesAfterRebuild_StaleAgain(t *testing.T) {
	res := freshness.Result{Applicable: true, Fresh: false, Rev: "abc123", Message: "rebuild needed"}
	probe := func() freshness.Result { return res }

	fresh, rebuild := newConsoleFreshnessChecker("main", probe, func() error { return nil }, func() (string, error) { return "", nil })
	if _, err := rebuild(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if _, isFresh, _ := fresh(); !isFresh {
		t.Fatalf("fresh() after the first rebuild reported stale, want fresh")
	}

	// The base branch advanced further after the rebuild — the probe now
	// reports a newer rev the checker never rebuilt against.
	res = freshness.Result{Applicable: true, Fresh: false, Rev: "def456", Message: "rebuild needed again"}

	if applicable, isFresh, msg := fresh(); !applicable || isFresh {
		t.Errorf("after the probe advanced past the rebuilt rev: applicable=%v fresh=%v msg=%q, want stale", applicable, isFresh, msg)
	}
}

// TestNewConsoleFreshnessChecker_AlreadyFresh_PassesThroughUnchanged
// verifies a probe result that is already fresh is returned as-is, with no
// rev-cache override applied — the caching path only ever matters for a
// stale verdict.
func TestNewConsoleFreshnessChecker_AlreadyFresh_PassesThroughUnchanged(t *testing.T) {
	res := freshness.Result{Applicable: true, Fresh: true, Rev: "abc123", Message: "fresh"}
	probe := func() freshness.Result { return res }

	fresh, _ := newConsoleFreshnessChecker("main", probe, func() error { return nil }, func() (string, error) { return "", nil })

	applicable, isFresh, msg := fresh()
	if !applicable || !isFresh || msg != "fresh" {
		t.Errorf("fresh() = (%v, %v, %q), want the probe's own fresh result unchanged", applicable, isFresh, msg)
	}
}

// TestNewConsoleFreshnessChecker_RebuildPropagatesPullAndBuildErrors
// verifies rebuild returns pull's or build's error without probing again or
// updating the cached rev — a failed rebuild must never look like a
// successful one on the next check.
func TestNewConsoleFreshnessChecker_RebuildPropagatesPullAndBuildErrors(t *testing.T) {
	probeCalls := 0
	probe := func() freshness.Result {
		probeCalls++
		return freshness.Result{Applicable: true, Fresh: false, Rev: "abc123"}
	}

	_, rebuild := newConsoleFreshnessChecker("main", probe, func() error { return errBoomFreshness }, func() (string, error) {
		t.Fatal("build called after pull failed")
		return "", nil
	})
	if _, err := rebuild(); err != errBoomFreshness {
		t.Errorf("rebuild() = %v, want the pull error", err)
	}
	if probeCalls != 0 {
		t.Errorf("probeCalls = %d, want 0 when pull fails before any probe", probeCalls)
	}
}
