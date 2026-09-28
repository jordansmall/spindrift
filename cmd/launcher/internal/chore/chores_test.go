package chore

import (
	"reflect"
	"testing"
)

func TestChores(t *testing.T) {
	cases := map[string][]string{
		"":        {},
		"  ":      {},
		"a":       {"a"},
		"a b  c":  {"a", "b", "c"},
		"a\tb\nc": {"a", "b", "c"},
	}
	for in, want := range cases {
		if got := Chores(in); !reflect.DeepEqual(got, want) {
			t.Errorf("Chores(%q) = %#v, want %#v", in, got, want)
		}
	}
}
