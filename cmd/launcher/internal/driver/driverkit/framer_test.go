package driverkit

import (
	"reflect"
	"testing"
)

func TestLineFramerSplitChunks(t *testing.T) {
	var got []string
	emit := func(line string) { got = append(got, line) }

	var f LineFramer
	f.Push([]byte("ab"), emit)
	f.Push([]byte("c\nde"), emit)
	f.Push([]byte("f\n"), emit)

	want := []string{"abc", "def"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestLineFramerTrailingPartialNotEmitted(t *testing.T) {
	var got []string
	emit := func(line string) { got = append(got, line) }

	var f LineFramer
	f.Push([]byte("partial"), emit)

	if len(got) != 0 {
		t.Fatalf("got %v, want no emitted lines before newline arrives", got)
	}

	f.Push([]byte(" line\n"), emit)
	want := []string{"partial line"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestLineFramerDrainReturnsPartialOnce(t *testing.T) {
	var f LineFramer
	f.Push([]byte("one\ntw"), func(string) {})
	if got := f.Drain(); got != "tw" {
		t.Errorf("Drain = %q, want %q", got, "tw")
	}
	if got := f.Drain(); got != "" {
		t.Errorf("second Drain = %q, want empty", got)
	}
}
