package freshness

import (
	"errors"
	"testing"
	"time"
)

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

// Probe's fetch-failure branch produces this Result shape, having never got as
// far as a rev or a tag. Despite the name, the test exercises the TipTag == ""
// guard, because RealizeTip no longer looks at res.Rev when deciding to realize.
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

// Probe's eval-error and tag-derive-error branches both produce this shape,
// having bailed before deriving a tag. A nix build here would only repeat the
// failure Probe's nix eval already hit. A guard that checks res.Rev == ""
// wrongly admits this case, so the guard checks res.TipTag == "" instead.
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

// Regression test for the launcher-only-stale bug (issue #1364): when the image
// matches but the launcher does not, RealizeTip must not kick off a background
// nix build of a tip image that is already loaded and fresh. It duplicates the
// empty-TipTag cases above so the launcher scenario has its own named test.
func TestRealizeTip_LauncherOnlyStaleNoOp(t *testing.T) {
	rf := NewRealizerFake()
	res := Result{Applicable: true, Fresh: false, ImageFresh: true, LauncherFresh: false, Rev: "revA", TipTag: ""}

	RealizeTip(rf, "/pwd", res, ".#packages.x86_64-linux.agent-image")

	select {
	case <-rf.Done:
		t.Fatal("Start was called, want no-op for a launcher-only-stale Result (image already fresh, nothing to realize)")
	case <-time.After(100 * time.Millisecond):
	}
	if calls := rf.CallsCopy(); len(calls) != 0 {
		t.Fatalf("len(calls) = %d, want 0 for a launcher-only-stale Result", len(calls))
	}
}

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

// The fake blocks inside Start so the test can prove RealizeTip is
// fire-and-forget rather than merely eventually async: it must return while
// Start is still blocked.
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

// A Start fork failure returns a nil wait function. This test guards against
// that nil wait reaching the background goroutine, where calling it would
// nil-deref.
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

// The trim mirrors the one Probe does immediately before its eval.Eval call.
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

func TestRealizeSync_NotApplicableNoOp(t *testing.T) {
	rf := NewRealizerFake()
	res := Result{Applicable: false, Fresh: false, Rev: "deadbeef"}

	if err := RealizeSync(rf, "/pwd", res, ".#packages.x86_64-linux.agent-image"); err != nil {
		t.Fatalf("RealizeSync() = %v, want nil for a no-op guard", err)
	}
	if calls := rf.CallsCopy(); len(calls) != 0 {
		t.Fatalf("len(calls) = %d, want 0 when Applicable is false", len(calls))
	}
}

func TestRealizeSync_FreshNoOp(t *testing.T) {
	rf := NewRealizerFake()
	res := Result{Applicable: true, Fresh: true, Rev: "deadbeef"}

	if err := RealizeSync(rf, "/pwd", res, ".#packages.x86_64-linux.agent-image"); err != nil {
		t.Fatalf("RealizeSync() = %v, want nil for a no-op guard", err)
	}
	if calls := rf.CallsCopy(); len(calls) != 0 {
		t.Fatalf("len(calls) = %d, want 0 when Fresh is true", len(calls))
	}
}

func TestRealizeSync_EmptyTipTagNoOp(t *testing.T) {
	rf := NewRealizerFake()
	res := Result{Applicable: true, Fresh: false, Rev: "somerev", TipTag: ""}

	if err := RealizeSync(rf, "/pwd", res, ".#packages.x86_64-linux.agent-image"); err != nil {
		t.Fatalf("RealizeSync() = %v, want nil for a no-op guard", err)
	}
	if calls := rf.CallsCopy(); len(calls) != 0 {
		t.Fatalf("len(calls) = %d, want 0 when TipTag is empty", len(calls))
	}
}

// Unlike RealizeTip, RealizeSync is synchronous, so the non-blocking read of
// Done must already succeed the moment RealizeSync returns.
func TestRealizeSync_BlocksUntilWaitCompletes(t *testing.T) {
	rf := NewRealizerFake()
	res := Result{Applicable: true, Fresh: false, Rev: "deadbeefcafe", TipTag: "spindrift:abc123"}

	if err := RealizeSync(rf, "/pwd", res, ".#packages.x86_64-linux.agent-image"); err != nil {
		t.Fatalf("RealizeSync() = %v, want nil on success", err)
	}

	select {
	case <-rf.Done:
	default:
		t.Fatal("Done did not fire by the time RealizeSync returned, want a synchronous wait")
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

// RealizeSync returns the error instead of logging it the way RealizeTip does,
// because its caller handles and logs the failure itself.
func TestRealizeSync_StartErrorReturnsErr(t *testing.T) {
	rf := NewRealizerFake()
	rf.StartErr = errors.New("fork failed: nix not found")
	res := Result{Applicable: true, Fresh: false, Rev: "deadbeefcafe", TipTag: "spindrift:abc123"}

	err := RealizeSync(rf, "/pwd", res, ".#packages.x86_64-linux.agent-image")
	if err == nil {
		t.Fatal("RealizeSync() = nil, want an error when Start fails to fork")
	}
	if calls := rf.CallsCopy(); len(calls) != 0 {
		t.Fatalf("len(calls) = %d, want 0 when Start fails to fork", len(calls))
	}
}

func TestRealizeSync_WaitErrorReturnsErr(t *testing.T) {
	rf := NewRealizerFake()
	rf.Err = errors.New("nix build failed: attribute not found")
	res := Result{Applicable: true, Fresh: false, Rev: "deadbeefcafe", TipTag: "spindrift:abc123"}

	err := RealizeSync(rf, "/pwd", res, ".#packages.x86_64-linux.agent-image")
	if err == nil {
		t.Fatal("RealizeSync() = nil, want an error when wait() fails")
	}
}
