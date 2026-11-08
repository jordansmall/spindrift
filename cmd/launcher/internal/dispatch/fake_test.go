package dispatch

import "testing"

// TestFake_DefaultsToSuccess verifies that a fresh Fake reports success on
// Run and Fix without any configuration, mirroring runner.Fake's zero-value
// (nil RunErr) success default.
func TestFake_DefaultsToSuccess(t *testing.T) {
	f := NewFake()

	if got := f.Run(); !got.ok() {
		t.Errorf("Run: want succeeded, got %+v", got)
	}
	if got := f.Fix(1, "detail"); !got.ok() {
		t.Errorf("Fix: want succeeded, got %+v", got)
	}
	if err := f.ResolveConflict("pr"); err != nil {
		t.Errorf("ResolveConflict: want nil, got %v", err)
	}
}

// TestFake_RecordsCalls verifies Fake records Run/Fix/ResolveConflict/Close
// invocations for assertions.
func TestFake_RecordsCalls(t *testing.T) {
	f := NewFake()

	f.Run()
	f.Run()
	f.Fix(1, "first")
	f.Fix(2, "second")
	f.ResolveConflict("pr-1")
	f.Close()

	if f.RunCalls != 2 {
		t.Errorf("RunCalls: got %d, want 2", f.RunCalls)
	}
	wantFix := []FixCall{{Pass: 1, CIFailureSummary: "first"}, {Pass: 2, CIFailureSummary: "second"}}
	if len(f.FixCalls) != len(wantFix) || f.FixCalls[0] != wantFix[0] || f.FixCalls[1] != wantFix[1] {
		t.Errorf("FixCalls: got %+v, want %+v", f.FixCalls, wantFix)
	}
	if len(f.ResolveConflictCalls) != 1 || f.ResolveConflictCalls[0] != "pr-1" {
		t.Errorf("ResolveConflictCalls: got %v", f.ResolveConflictCalls)
	}
	if f.CloseCalls != 1 {
		t.Errorf("CloseCalls: got %d, want 1", f.CloseCalls)
	}
}

func TestFake_HooksFireOnFixAndResolveConflict(t *testing.T) {
	f := NewFake()
	var fix, resolve []int
	f.On("Fix", func(n int) { fix = append(fix, n) })
	f.On("ResolveConflict", func(n int) { resolve = append(resolve, n) })

	f.Fix(1, "")
	f.Fix(2, "")
	_ = f.ResolveConflict("pr")

	if len(fix) != 2 || fix[1] != 2 || len(resolve) != 1 || resolve[0] != 1 {
		t.Errorf("fix hook calls = %v, resolve hook calls = %v; want [1 2] and [1]", fix, resolve)
	}
}
