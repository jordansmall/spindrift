package forge

import (
	"strings"
	"testing"
)

func TestTailBufferKeepsTheEndOfALongStream(t *testing.T) {
	var tb TailBuffer
	chunk := strings.Repeat("x", 1000)
	for i := 0; i < 100; i++ {
		_, _ = tb.Write([]byte(chunk))
	}
	_, _ = tb.Write([]byte("END"))
	got := tb.String()
	if len(got) > 2*JobLogKeepBytes {
		t.Errorf("TailBuffer holds %d bytes, want at most %d", len(got), 2*JobLogKeepBytes)
	}
	if len(got) < JobLogKeepBytes || !strings.HasSuffix(got, "xEND") {
		t.Errorf("TailBuffer lost the end of the stream (len %d)", len(got))
	}
}

func TestTailBufferKeepsAShortStreamWhole(t *testing.T) {
	var tb TailBuffer
	_, _ = tb.Write([]byte("a\n"))
	_, _ = tb.Write([]byte("b\n"))
	if got := tb.String(); got != "a\nb\n" {
		t.Errorf("TailBuffer = %q, want the whole stream", got)
	}
}
