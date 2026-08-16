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
