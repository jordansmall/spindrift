package freshness

import (
	"errors"
	"testing"
	"time"
)

// TestRealizeTip_NotApplicableNoOp verifies RealizeTip never calls Start
// when res.Applicable is false — there's no rebuild-needed verdict to act
// on at all.
func TestRealizeTip_NotApplicableNoOp(t *testing.T) {
	rf := NewRealizerFake()
	res := Result{Applicable: false, Fresh: false, Rev: "deadbeef"}

	RealizeTip(rf, "/pwd", res, ".#packages.x86_64-linux.agent-image")

	select {
	case <-rf.Done:
		t.Fatal("Start was called, want no-op when Applicable is false")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestRealizeTip_FreshNoOp verifies RealizeTip never calls Start when
// res.Fresh is true — the loaded image already matches the base tip, so
// nothing needs realizing.
func TestRealizeTip_FreshNoOp(t *testing.T) {
	rf := NewRealizerFake()
	res := Result{Applicable: true, Fresh: true, Rev: "deadbeef"}

	RealizeTip(rf, "/pwd", res, ".#packages.x86_64-linux.agent-image")

	select {
	case <-rf.Done:
		t.Fatal("Start was called, want no-op when Fresh is true")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestRealizeTip_EmptyRevNoOp verifies RealizeTip never calls Start on a
// Result with an empty TipTag whose Rev also happens to be empty — the shape
// Probe's fetch-failure branch produces (it couldn't even fetch the base
// tip, so it never got as far as deriving a rev or a tag). It's exercising
// the TipTag == "" guard, not a distinct Rev-based guard: RealizeTip no
// longer looks at res.Rev at all when deciding whether to realize.
func TestRealizeTip_EmptyRevNoOp(t *testing.T) {
	rf := NewRealizerFake()
	res := Result{Applicable: true, Fresh: false, Rev: ""}

	RealizeTip(rf, "/pwd", res, ".#packages.x86_64-linux.agent-image")

	select {
	case <-rf.Done:
		t.Fatal("Start was called, want no-op when Rev is empty")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestRealizeTip_NonEmptyRevEmptyTipTagNoOp verifies RealizeTip never calls
// Start when res carries a real, non-empty Rev but an empty TipTag — the
// shape Probe's eval-error and tag-derive-error branches both produce
// (Applicable=true, Fresh=false, Rev set, TipTag==""), since Probe bailed
// before ever deriving a tag. Calling Start (nix build) here would just
// repeat the same failure Probe's nix eval already hit, for nothing. A guard
// that only checks res.Rev == "" wrongly admits this case; the correct guard
// checks res.TipTag == "" instead.
func TestRealizeTip_NonEmptyRevEmptyTipTagNoOp(t *testing.T) {
	rf := NewRealizerFake()
	res := Result{Applicable: true, Fresh: false, Rev: "somerev", TipTag: ""}

	RealizeTip(rf, "/pwd", res, ".#packages.x86_64-linux.agent-image")

	select {
	case <-rf.Done:
		t.Fatal("Start was called, want no-op when Rev is non-empty but TipTag is empty (Probe's eval-error/tag-derive-error shape)")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestRealizeTip_CallsRealizeOnce verifies RealizeTip calls Start exactly
// once with (pwd, res.Rev, trimmed-attr) when the verdict is a genuine
// rebuild-needed at a known rev. It synchronizes on the fake's Done channel
// rather than sleeping.
func TestRealizeTip_CallsRealizeOnce(t *testing.T) {
	rf := NewRealizerFake()
	res := Result{Applicable: true, Fresh: false, Rev: "deadbeefcafe", TipTag: "spindrift:abc123"}

	RealizeTip(rf, "/pwd", res, ".#packages.x86_64-linux.agent-image")

	select {
	case <-rf.Done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start was not called within timeout")
	}

	calls := rf.CallsCopy()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}
	want := FakeCall{Pwd: "/pwd", Rev: "deadbeefcafe", Attr: "packages.x86_64-linux.agent-image"}
	if calls[0] != want {
		t.Errorf("call = %+v, want %+v", calls[0], want)
	}
}

// TestRealizeTip_ReturnsBeforeRealizeCompletes proves RealizeTip is genuinely
// fire-and-forget, not just "eventually async": it gives the fake a channel
// Start blocks reading from before returning, calls RealizeTip, and
// confirms the call returns without unblocking that channel first. Only
// after the test explicitly unblocks it does the fake's Done channel fire.
func TestRealizeTip_ReturnsBeforeRealizeCompletes(t *testing.T) {
	block := make(chan struct{})
	rf := NewRealizerFake()
	rf.Block = block
	res := Result{Applicable: true, Fresh: false, Rev: "deadbeefcafe", TipTag: "spindrift:abc123"}

	returned := make(chan struct{})
	go func() {
		RealizeTip(rf, "/pwd", res, ".#packages.x86_64-linux.agent-image")
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("RealizeTip did not return promptly")
	}

	// Start must still be blocked at this point — confirm Done hasn't
	// fired yet.
	select {
	case <-rf.Done:
		t.Fatal("Start completed before being unblocked")
	default:
	}

	close(block)

	select {
	case <-rf.Done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not complete after being unblocked")
	}
}

// TestRealizeTip_StartErrorLogsAndNoOp verifies RealizeTip handles a Start
// fork failure (nil wait, non-nil err) by returning promptly without ever
// invoking the (nil) wait function — the bug this test guards against is a
// nil wait reaching `go func(){ wait() }()`, which would nil-deref. It
// confirms Done never fires (the background goroutine, and hence any call to
// wait, never runs) and that CallsCopy is empty (Start never forked, so
// nothing was durably recorded).
func TestRealizeTip_StartErrorLogsAndNoOp(t *testing.T) {
	rf := NewRealizerFake()
	rf.StartErr = errors.New("fork failed: nix not found")
	res := Result{Applicable: true, Fresh: false, Rev: "deadbeefcafe", TipTag: "spindrift:abc123"}

	done := make(chan struct{})
	go func() {
		RealizeTip(rf, "/pwd", res, ".#packages.x86_64-linux.agent-image")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RealizeTip did not return promptly")
	}

	select {
	case <-rf.Done:
		t.Fatal("Start's wait function ran, want no-op when Start fails to fork")
	case <-time.After(100 * time.Millisecond):
	}

	if calls := rf.CallsCopy(); len(calls) != 0 {
		t.Fatalf("len(calls) = %d, want 0 when Start fails to fork", len(calls))
	}
}

// TestRealizeTip_TrimsFlakeAttrPrefix verifies flakeImageAttr passed with a
// ".#" prefix is trimmed before being passed to Start, mirroring Probe's
// own attr-trim immediately before its eval.Eval call.
func TestRealizeTip_TrimsFlakeAttrPrefix(t *testing.T) {
	rf := NewRealizerFake()
	res := Result{Applicable: true, Fresh: false, Rev: "deadbeefcafe", TipTag: "spindrift:abc123"}

	RealizeTip(rf, "/pwd", res, ".#packages.x86_64-linux.agent-image")

	select {
	case <-rf.Done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start was not called within timeout")
	}

	calls := rf.CallsCopy()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}
	if calls[0].Attr != "packages.x86_64-linux.agent-image" {
		t.Errorf("Attr = %q, want the \".#\" prefix trimmed", calls[0].Attr)
	}
}
