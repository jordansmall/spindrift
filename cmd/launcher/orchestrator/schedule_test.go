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
// the same batch.
func TestScheduleSlices_GenuinelyDisjointLeasesConcurrent(t *testing.T) {
	slices := []ManifestSlice{
		{Name: "a", FileLeases: []string{"cmd/x/a.go"}},
		{Name: "b", FileLeases: []string{"cmd/y/b.go"}},
	}
	got := scheduleNames(t, slices)
	assertBatches(t, got, [][]string{{"a", "b"}})
}
