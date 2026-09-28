package chore_test

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/ledger"
)

func TestNextScope(t *testing.T) {
	tests := []struct {
		name      string
		prev      ledger.State
		head      string
		files     []string
		sliceSize int
		want      chore.Scope
	}{
		{
			name:      "first run: no LastSwept, no DiffRange, slice starts at the top",
			prev:      ledger.State{},
			head:      "headsha",
			files:     []string{"c", "a", "b"},
			sliceSize: 2,
			want: chore.Scope{
				Head: "headsha", DiffRange: "",
				Slice: []string{"a", "b"}, NextCursor: "b",
			},
		},
		{
			name:      "diff range spans lastSwept..head when they differ",
			prev:      ledger.State{LastSwept: "oldsha", Cursor: "b"},
			head:      "newsha",
			files:     []string{"a", "b", "c", "d", "e"},
			sliceSize: 2,
			want: chore.Scope{
				Head: "newsha", DiffRange: "oldsha..newsha",
				Slice: []string{"c", "d"}, NextCursor: "d",
			},
		},
		{
			name:      "no diff range when lastSwept equals head",
			prev:      ledger.State{LastSwept: "samesha", Cursor: ""},
			head:      "samesha",
			files:     []string{"a", "b"},
			sliceSize: 2,
			want: chore.Scope{
				Head: "samesha", DiffRange: "",
				Slice: []string{"a", "b"}, NextCursor: "",
			},
		},
		{
			name:      "cursor mid-tree slices the next stretch after it",
			prev:      ledger.State{LastSwept: "s", Cursor: "b"},
			head:      "h",
			files:     []string{"a", "b", "c", "d", "e"},
			sliceSize: 2,
			want: chore.Scope{
				Head: "h", DiffRange: "s..h",
				Slice: []string{"c", "d"}, NextCursor: "d",
			},
		},
		{
			name:      "cursor at the last path wraps to the start",
			prev:      ledger.State{LastSwept: "s", Cursor: "e"},
			head:      "h",
			files:     []string{"a", "b", "c", "d", "e"},
			sliceSize: 2,
			want: chore.Scope{
				Head: "h", DiffRange: "s..h",
				Slice: []string{"a", "b"}, NextCursor: "b",
			},
		},
		{
			name:      "cursor past the last path also wraps to the start",
			prev:      ledger.State{LastSwept: "s", Cursor: "zzz"},
			head:      "h",
			files:     []string{"a", "b", "c", "d", "e"},
			sliceSize: 2,
			want: chore.Scope{
				Head: "h", DiffRange: "s..h",
				Slice: []string{"a", "b"}, NextCursor: "b",
			},
		},
		{
			name:      "reaching the final path resets NextCursor to empty",
			prev:      ledger.State{LastSwept: "s", Cursor: "c"},
			head:      "h",
			files:     []string{"a", "b", "c", "d", "e"},
			sliceSize: 2,
			want: chore.Scope{
				Head: "h", DiffRange: "s..h",
				Slice: []string{"d", "e"}, NextCursor: "",
			},
		},
		{
			name:      "empty tree yields an empty slice and no cursor",
			prev:      ledger.State{},
			head:      "h",
			files:     nil,
			sliceSize: 40,
			want: chore.Scope{
				Head: "h", DiffRange: "",
				Slice: nil, NextCursor: "",
			},
		},
		{
			name:      "sliceSize larger than the tree covers it all in one run",
			prev:      ledger.State{},
			head:      "h",
			files:     []string{"c", "a", "b"},
			sliceSize: 40,
			want: chore.Scope{
				Head: "h", DiffRange: "",
				Slice: []string{"a", "b", "c"}, NextCursor: "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chore.NextScope(tt.prev, tt.head, tt.files, tt.sliceSize)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("NextScope(%+v, %q, %v, %d) = %+v, want %+v",
					tt.prev, tt.head, tt.files, tt.sliceSize, got, tt.want)
			}
		})
	}
}
