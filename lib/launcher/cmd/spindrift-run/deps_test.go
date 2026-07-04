package main

import (
	"reflect"
	"sort"
	"testing"
)

func TestParseBlockers(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []int
	}{
		{
			name: "empty body",
			body: "",
			want: nil,
		},
		{
			name: "no blockers",
			body: "Just implement the feature as described.",
			want: nil,
		},
		{
			name: "inline depends on",
			body: "Depends on #1",
			want: []int{1},
		},
		{
			name: "inline depends on lowercase",
			body: "depends on #42",
			want: []int{42},
		},
		{
			name: "inline blocked by",
			body: "blocked by #7",
			want: []int{7},
		},
		{
			name: "inline blocked by with colon",
			body: "Blocked by: #7",
			want: []int{7},
		},
		{
			name: "inline multiple on one line",
			body: "depends on #1, depends on #2",
			want: []int{1, 2},
		},
		{
			name: "header-plus-list single item",
			body: "## Blocked by\n\n- #1",
			want: []int{1},
		},
		{
			name: "header-plus-list with description",
			body: "## Blocked by\n\n- #59 (Go launcher: config, query, single-wave, report)",
			want: []int{59},
		},
		{
			name: "header-plus-list multiple items",
			body: "## Blocked by\n\n- #1\n- #2\n- #3",
			want: []int{1, 2, 3},
		},
		{
			name: "header-plus-list depends-on variant",
			body: "## Depends on\n\n- #10",
			want: []int{10},
		},
		{
			name: "header-plus-list case insensitive",
			body: "## BLOCKED BY\n\n- #5",
			want: []int{5},
		},
		{
			name: "section ends at next heading",
			body: "## Blocked by\n\n- #1\n\n## Next section\n\n- #2",
			want: []int{1},
		},
		{
			name: "inline before header section",
			body: "Depends on #99\n\n## Blocked by\n\n- #1",
			want: []int{99, 1},
		},
		{
			name: "h3 header works",
			body: "### Blocked by\n\n- #3",
			want: []int{3},
		},
		{
			name: "list item with asterisk",
			body: "## Blocked by\n\n* #4",
			want: []int{4},
		},
		{
			name: "inline not in blocked-by section",
			body: "## Background\n\nThis depends on #99 for context.",
			want: []int{99},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseBlockers(tc.body)
			sortInts(got)
			sortInts(tc.want)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseBlockers(%q)\n  got  %v\n  want %v", tc.body, got, tc.want)
			}
		})
	}
}

func sortInts(s []int) {
	sort.Ints(s)
}

func TestDetectCycle(t *testing.T) {
	tests := []struct {
		name      string
		deps      map[int][]int
		nodes     []int
		wantCycle bool
	}{
		{
			name:      "no deps",
			deps:      map[int][]int{},
			nodes:     []int{1, 2},
			wantCycle: false,
		},
		{
			name:      "linear chain",
			deps:      map[int][]int{2: {1}},
			nodes:     []int{1, 2},
			wantCycle: false,
		},
		{
			name:      "diamond",
			deps:      map[int][]int{3: {1}, 4: {1, 2}},
			nodes:     []int{1, 2, 3, 4},
			wantCycle: false,
		},
		{
			name:      "simple cycle",
			deps:      map[int][]int{1: {2}, 2: {1}},
			nodes:     []int{1, 2},
			wantCycle: true,
		},
		{
			name:      "three-node cycle",
			deps:      map[int][]int{1: {3}, 2: {1}, 3: {2}},
			nodes:     []int{1, 2, 3},
			wantCycle: true,
		},
		{
			name: "external blocker not in batch — not a cycle",
			// issue 2 depends on issue 99 which is not in the batch
			deps:      map[int][]int{2: {99}},
			nodes:     []int{1, 2},
			wantCycle: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cycle := detectCycle(tc.deps, tc.nodes)
			if tc.wantCycle && cycle == 0 {
				t.Errorf("detectCycle: expected a cycle, got none")
			}
			if !tc.wantCycle && cycle != 0 {
				t.Errorf("detectCycle: expected no cycle, got issue #%d", cycle)
			}
		})
	}
}
