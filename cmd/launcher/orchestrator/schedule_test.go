package main

import "testing"

// batchNames maps a batch of ManifestSlice to their names, for terse
// assertions below.
func batchNames(batch []ManifestSlice) []string {
	names := make([]string, len(batch))
	for i, s := range batch {
		names[i] = s.Name
	}
	return names
}

// scheduleNames runs scheduleSlices and flattens the result to a slice of
// per-batch name slices, for terse assertions below.
func scheduleNames(t *testing.T, slices []ManifestSlice) [][]string {
	t.Helper()
	batches := scheduleSlices(slices)
	got := make([][]string, len(batches))
	for i, b := range batches {
		got[i] = batchNames(b)
	}
	return got
}

func assertBatches(t *testing.T, got [][]string, want [][]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("batch count = %d, want %d; got=%v want=%v", len(got), len(want), got, want)
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("batch %d size = %d, want %d; got=%v want=%v", i, len(got[i]), len(want[i]), got, want)
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("batch %d[%d] = %q, want %q; got=%v want=%v", i, j, got[i][j], want[i][j], got, want)
			}
		}
	}
}

func TestScheduleSlices_DisjointLeasesConcurrent(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", FileLeases: []string{"a.go"}},
		{Name: "b", FileLeases: []string{"b.go"}},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"a", "b"}})
}

func TestScheduleSlices_OverlappingLeasesSequenced(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", FileLeases: []string{"shared.go"}},
		{Name: "b", FileLeases: []string{"shared.go"}},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"a"}, {"b"}})
}

func TestScheduleSlices_EmptyLeasesNeverSharesWithNonEmpty(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", FileLeases: nil},
		{Name: "b", FileLeases: []string{"b.go"}},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"a"}, {"b"}})
}

func TestScheduleSlices_TwoEmptyLeasesNeverShare(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", FileLeases: nil},
		{Name: "b", FileLeases: nil},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"a"}, {"b"}})
}

func TestScheduleSlices_DependsOnEarlierSlice(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", FileLeases: []string{"a.go"}},
		{Name: "b", FileLeases: []string{"b.go"}, DependsOn: []string{"a"}},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"a"}, {"b"}})
}

func TestScheduleSlices_ThreeSliceChain(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", FileLeases: []string{"a.go"}},
		{Name: "b", FileLeases: []string{"b.go"}, DependsOn: []string{"a"}},
		{Name: "c", FileLeases: []string{"c.go"}, DependsOn: []string{"b"}},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"a"}, {"b"}, {"c"}})
}

func TestScheduleSlices_DanglingDependsOnIgnored(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", FileLeases: []string{"a.go"}, DependsOn: []string{"nonexistent"}},
		{Name: "b", FileLeases: []string{"b.go"}},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"a", "b"}})
}

func TestScheduleSlices_EmptyInput(t *testing.T) {
	if got := scheduleSlices(nil); len(got) != 0 {
		t.Fatalf("scheduleSlices(nil) = %v, want empty", got)
	}
	if got := scheduleSlices([]ManifestSlice{}); len(got) != 0 {
		t.Fatalf("scheduleSlices([]) = %v, want empty", got)
	}
}

func TestScheduleSlices_ManifestOrderPreservedWithinBatch(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", FileLeases: []string{"a.go"}},
		{Name: "b", FileLeases: []string{"b.go"}},
		{Name: "c", FileLeases: []string{"c.go"}},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"a", "b", "c"}})
}

// TestScheduleSlices_ForwardDeclaredDependsOn covers a DependsOn edge naming
// a slice declared LATER in the manifest than the dependent -- the
// dependency must still be honored (b before a), not silently dropped just
// because batchIndexOf wasn't populated yet in raw declaration order.
func TestScheduleSlices_ForwardDeclaredDependsOn(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", DependsOn: []string{"b"}},
		{Name: "b"},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"b"}, {"a"}})
}

// TestScheduleSlices_OverlappingDirectoryLeaseSequenced covers a lease that
// names a directory containing another slice's file lease -- these are not
// provably disjoint and must be sequenced into separate batches even though
// the strings aren't identical.
func TestScheduleSlices_OverlappingDirectoryLeaseSequenced(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", FileLeases: []string{"cmd/x"}},
		{Name: "b", FileLeases: []string{"cmd/x/dispatch.go"}},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"a"}, {"b"}})
}

// TestScheduleSlices_NormalizedEquivalentLeasesSequenced covers two leases
// that name the same path in different but equivalent forms ("./a.go" vs
// "a.go") -- these must be treated as the same path and sequenced into
// separate batches.
func TestScheduleSlices_NormalizedEquivalentLeasesSequenced(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", FileLeases: []string{"./a.go"}},
		{Name: "b", FileLeases: []string{"a.go"}},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"a"}, {"b"}})
}

// TestScheduleSlices_GenuinelyDisjointLeasesConcurrent guards against
// over-correcting the lease-overlap fix into always-sequential: leases that
// are neither equal nor prefix-related after normalization must still join
// the same batch. This is a regression guard -- it already passes today.
func TestScheduleSlices_GenuinelyDisjointLeasesConcurrent(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", FileLeases: []string{"cmd/x/a.go"}},
		{Name: "b", FileLeases: []string{"cmd/y/b.go"}},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"a", "b"}})
}

// TestScheduleSlices_RootLeaseOverlapsEverything covers a lease of "."
// (whole repo root) -- it must be treated as unprovably overlapping with
// every other lease, never scheduled concurrently alongside anything else.
func TestScheduleSlices_RootLeaseOverlapsEverything(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", FileLeases: []string{"."}},
		{Name: "b", FileLeases: []string{"cmd/x.go"}},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"a"}, {"b"}})
}

// TestScheduleSlices_EmptyStringLeaseOverlapsEverything covers a lease of ""
// -- path.Clean("") == "." so this must behave identically to an explicit
// "." lease: unprovably overlapping with every other lease.
func TestScheduleSlices_EmptyStringLeaseOverlapsEverything(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", FileLeases: []string{""}},
		{Name: "b", FileLeases: []string{"cmd/x.go"}},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"a"}, {"b"}})
}

// TestScheduleSlices_AbsoluteVsRelativeLeaseOverlap covers an absolute lease
// compared against a relative lease naming logically the same file -- with
// no repoRoot available to this package, there's no reliable way to prove
// they're disjoint, so the pair must be treated as overlapping.
func TestScheduleSlices_AbsoluteVsRelativeLeaseOverlap(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", FileLeases: []string{"/work/cmd/x.go"}},
		{Name: "b", FileLeases: []string{"cmd/x.go"}},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"a"}, {"b"}})
}

// TestScheduleSlices_ParentEscapeLeaseOverlapsEverything covers a lease
// containing ".." that escapes the repo root -- it can't be reasoned about
// against an ordinary relative lease, so it must be treated as unprovably
// overlapping with every other lease.
func TestScheduleSlices_ParentEscapeLeaseOverlapsEverything(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", FileLeases: []string{"../etc/passwd"}},
		{Name: "b", FileLeases: []string{"cmd/x.go"}},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"a"}, {"b"}})
}

// TestDependencyOrder_CycleFallbackTerminates covers dependencyOrder's
// cycle-fallback branch (a full scan makes no progress) with a genuine
// 2-slice cycle: a depends on b, b depends on a. It must terminate without
// hanging or panicking and return both slices, though the exact tie-break
// order beyond declaration order isn't pinned here. Both slices must also be
// reported via forceSolo, since both were placed by the fallback branch.
func TestDependencyOrder_CycleFallbackTerminates(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", DependsOn: []string{"b"}},
		{Name: "b", DependsOn: []string{"a"}},
	}
	got, forceSolo := dependencyOrder(slices)
	if len(got) != 2 {
		t.Fatalf("dependencyOrder(cycle) returned %d slices, want 2; got=%v", len(got), batchNames(got))
	}
	names := map[string]bool{got[0].Name: true, got[1].Name: true}
	if !names["a"] || !names["b"] {
		t.Fatalf("dependencyOrder(cycle) = %v, want both a and b present", batchNames(got))
	}
	if !forceSolo["a"] || !forceSolo["b"] {
		t.Fatalf("dependencyOrder(cycle) forceSolo = %v, want both a and b true", forceSolo)
	}
}

// TestScheduleSlices_DependencyCycleDoesNotHang covers scheduleSlices at the
// end-to-end level with the same 2-slice cycle -- it must terminate and
// produce some deterministic batch assignment holding both slices, whatever
// that assignment actually is (one batch or two).
func TestScheduleSlices_DependencyCycleDoesNotHang(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", DependsOn: []string{"b"}},
		{Name: "b", DependsOn: []string{"a"}},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"a"}, {"b"}})
}

// TestScheduleSlices_GlobLeaseTreatedAsOverlapping covers a lease containing
// glob metacharacters ("*", "?", "["). This package only ever compares
// leases as plain strings (equality/prefix), so a glob lease can never be
// proven disjoint from anything by that logic -- it must always be treated
// as overlapping, forcing both slices in each pair into separate solo
// batches.
func TestScheduleSlices_GlobLeaseTreatedAsOverlapping(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", FileLeases: []string{"cmd/**"}},
		{Name: "b", FileLeases: []string{"cmd/launcher/x.go"}},
		{Name: "c", FileLeases: []string{"*.go"}},
		{Name: "d", FileLeases: []string{"main.go"}},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"a"}, {"b"}, {"c"}, {"d"}})
}

// TestScheduleSlices_CycleFallbackSlicesForcedSolo covers a 3-slice
// dependency cycle (a->b->c->a) with disjoint, non-empty FileLeases: since
// dependencyOrder's cycle fallback can't establish a safe processing order
// for any of them, every one of the three must land in its own solo batch
// rather than being allowed to co-batch under lease rules alone (issue
// #2060 review finding).
func TestScheduleSlices_CycleFallbackSlicesForcedSolo(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", FileLeases: []string{"a.go"}, DependsOn: []string{"b"}},
		{Name: "b", FileLeases: []string{"b.go"}, DependsOn: []string{"c"}},
		{Name: "c", FileLeases: []string{"c.go"}, DependsOn: []string{"a"}},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"a"}, {"b"}, {"c"}})
}

// TestScheduleSlices_CycleContagionDoesNotSpreadUnsafely covers a case where
// only two slices (a, b) form a genuine cycle, but a third (c) merely
// depends on one of them -- dependencyOrder's fallback places all three via
// the fallback branch (since a full scan makes no progress once a<->b are
// stuck), so c must also be forced solo even though it isn't itself part of
// the cycle.
func TestScheduleSlices_CycleContagionDoesNotSpreadUnsafely(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "c", FileLeases: []string{"c.go"}, DependsOn: []string{"a"}},
		{Name: "a", FileLeases: []string{"a.go"}, DependsOn: []string{"b"}},
		{Name: "b", FileLeases: []string{"b.go"}, DependsOn: []string{"a"}},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"c"}, {"a"}, {"b"}})
}
