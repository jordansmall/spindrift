package main

import (
	"testing"
	"time"
)

func TestResultEvent(t *testing.T) {
	b, err := ResultEvent("a \"quoted\"\nline <b>")
	if err != nil {
		t.Fatal(err)
	}
	const want = "{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"a \\\"quoted\\\"\\nline \\u003cb\\u003e\"}],\"stopReason\":\"stop\"}}\n"
	if string(b) != want {
		t.Errorf("ResultEvent = %q, want %q", b, want)
	}
	// It is the inverse of the decoder: the host reads the text back from it.
	evs := NewDecoder(time.Time{})(string(b[:len(b)-1]))
	if len(evs) != 1 || evs[0].Text != "a \"quoted\"\nline <b>" {
		t.Errorf("decode(ResultEvent) = %+v", evs)
	}
}
