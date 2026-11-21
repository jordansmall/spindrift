package main

import (
	"strings"
	"testing"
)

const (
	deltaA = `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"he"}}`
	deltaB = `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"llo"}}`
	msgEnd = `{"type":"message_end","message":{"role":"assistant","content":[]}}`
)

func filtered(t *testing.T, in string) (string, *strings.Builder) {
	t.Helper()
	var out strings.Builder
	f := NewStreamFilter(&out)
	if _, err := f.Write([]byte(in)); err != nil {
		t.Fatal(err)
	}
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	first := out.String()
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	return first, &out
}

func TestStreamFilter_DropsDeltasOfAFinishedMessage(t *testing.T) {
	got, _ := filtered(t, deltaA+"\n"+deltaB+"\n"+msgEnd+"\n")
	if got != msgEnd+"\n" {
		t.Errorf("got %q, want only the message_end", got)
	}
}

func TestStreamFilter_FlushWritesOpenMessageCoalescedAndIsIdempotent(t *testing.T) {
	first, out := filtered(t, deltaA+"\n"+deltaB+"\n")
	if !strings.Contains(first, `"delta":"hello"`) || strings.Count(first, "\n") != 1 {
		t.Errorf("got %q, want one coalesced delta line", first)
	}
	if out.String() != first {
		t.Errorf("second Flush wrote more: %q", out.String())
	}
}

func TestStreamFilter_FlushKeepsTrailingPartialLine(t *testing.T) {
	first, _ := filtered(t, `{"type":"x"`)
	if first != `{"type":"x"` {
		t.Errorf("got %q", first)
	}
}
