package forge_test

import (
	"errors"
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

func issueNumbers(issues []forge.Issue) []string {
	nums := make([]string, len(issues))
	for i, iss := range issues {
		nums[i] = iss.Number
	}
	return nums
}

func TestDepSource_String(t *testing.T) {
	cases := []struct {
		name   string
		source forge.DepSource
		want   string
	}{
		{"native", forge.DepSourceNative, "native"},
		{"body", forge.DepSourceBody, "body"},
		{"unknown", forge.DepSourceUnknown, "unknown"},
		{"out of range", forge.DepSource(99), "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.source.String(); got != c.want {
				t.Errorf("String() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestMergeLabeledIssues_Dedup(t *testing.T) {
	calls := map[string][]forge.Issue{
		"a": {{Number: "1"}, {Number: "2"}},
		"b": {{Number: "2"}, {Number: "3"}},
	}
	got, err := forge.MergeLabeledIssues("test", []string{"a", "b"}, func(label string) ([]forge.Issue, error) {
		return calls[label], nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	nums := issueNumbers(got)
	want := []string{"3", "2", "1"}
	if !slices.Equal(nums, want) {
		t.Fatalf("nums = %v, want %v", nums, want)
	}
}

func TestMergeLabeledIssues_PartialFailure(t *testing.T) {
	got, err := forge.MergeLabeledIssues("test", []string{"good", "bad"}, func(label string) ([]forge.Issue, error) {
		if label == "bad" {
			return nil, errors.New("boom")
		}
		return []forge.Issue{{Number: "5"}}, nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 1 || got[0].Number != "5" {
		t.Fatalf("got = %v, want one issue #5", got)
	}
}

func TestMergeLabeledIssues_AllFailed(t *testing.T) {
	errA := errors.New("label a failed")
	errB := errors.New("label b failed")
	_, err := forge.MergeLabeledIssues("test", []string{"a", "b"}, func(label string) ([]forge.Issue, error) {
		if label == "a" {
			return nil, errA
		}
		return nil, errB
	})
	if err == nil {
		t.Fatal("err = nil, want error")
	}
	if !errors.Is(err, errB) {
		t.Fatalf("err = %v, want wrapping errB (last label's error)", err)
	}
	if errors.Is(err, errA) {
		t.Fatalf("err = %v, wraps errA; helper must keep the last label's error, not the first", err)
	}
}

func TestMergeLabeledIssues_EmptyLabels(t *testing.T) {
	calls := 0
	got, err := forge.MergeLabeledIssues("test", nil, func(label string) ([]forge.Issue, error) {
		calls++
		return nil, nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != nil {
		t.Fatalf("got = %v, want nil", got)
	}
	if calls != 0 {
		t.Fatalf("calls = %d, want 0", calls)
	}
}

func TestMergeLabeledIssues_NumericSort(t *testing.T) {
	// Ascending, interleaved pages: the merge must re-sort, not concatenate.
	calls := map[string][]forge.Issue{
		"a": {{Number: "2"}, {Number: "10"}},
		"b": {{Number: "9"}, {Number: "11"}},
	}
	got, err := forge.MergeLabeledIssues("test", []string{"a", "b"}, func(label string) ([]forge.Issue, error) {
		return calls[label], nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	want := []string{"11", "10", "9", "2"}
	if nums := issueNumbers(got); !slices.Equal(nums, want) {
		t.Fatalf("nums = %v, want %v (numeric, not lexical)", nums, want)
	}
}

func TestRef(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		source forge.DepSource
		want   string
	}{
		{"native", "42", forge.DepSourceNative, "#42 (native)"},
		{"body", "43", forge.DepSourceBody, "#43 (body)"},
		{"unknown", "44", forge.DepSourceUnknown, "#44 (unknown)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := forge.Ref(c.id, c.source); got != c.want {
				t.Errorf("Ref(%q, %v) = %q, want %q", c.id, c.source, got, c.want)
			}
		})
	}
}
