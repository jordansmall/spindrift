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
	if len(got) > 2*JobLogKeepBytes+MaxLogExcerptBytes {
		t.Errorf("TailBuffer holds %d bytes, want at most %d", len(got), 2*JobLogKeepBytes+MaxLogExcerptBytes)
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

func TestTailBufferKeepsErrorLinesOfTheDroppedHead(t *testing.T) {
	var tb TailBuffer
	_, _ = tb.Write([]byte("ts pkg> --- FAIL: TestEarly\nts pkg> error: the cause\nordinary head line\n"))
	for i := 0; i < 300; i++ {
		_, _ = tb.Write([]byte(strings.Repeat("x", 99) + "\n"))
	}
	_, _ = tb.Write([]byte("END\n"))
	got := tb.String()
	if !strings.HasPrefix(got, "ts pkg> --- FAIL: TestEarly\nts pkg> error: the cause\n") {
		t.Errorf("TailBuffer lost the dropped head's error lines: %.120q", got)
	}
	if strings.Contains(got, "ordinary head line") {
		t.Error("TailBuffer kept an ordinary line of the dropped head")
	}
	if !strings.HasSuffix(got, "x\nEND\n") {
		t.Error("TailBuffer lost the end of the stream")
	}
}

func TestTailBufferBoundsTheKeptErrorLines(t *testing.T) {
	var tb TailBuffer
	for i := 0; i < 2000; i++ {
		_, _ = tb.Write([]byte("error: repeated failure line that is long enough to add up\n"))
	}
	if got := tb.String(); len(got) > 2*JobLogKeepBytes+MaxLogExcerptBytes {
		t.Errorf("TailBuffer holds %d bytes, want at most %d", len(got), 2*JobLogKeepBytes+MaxLogExcerptBytes)
	}
}

// A trim backs off to the last newline before its cut; with that newline at
// the buffer's start it keeps more than 2*JobLogKeepBytes until the next write.
func TestTailBufferBoundsTheTailWhenOnlyAnEarlyNewlineExists(t *testing.T) {
	const chunkSize = 1000
	var tb TailBuffer
	_, _ = tb.Write([]byte("x\n" + strings.Repeat("y", chunkSize-2)))
	for i := 0; i < 100; i++ {
		_, _ = tb.Write([]byte(strings.Repeat("y", chunkSize)))
		if got := tb.String(); len(got) > 2*JobLogKeepBytes+chunkSize {
			t.Fatalf("after write %d TailBuffer holds %d bytes, want at most %d", i, len(got), 2*JobLogKeepBytes+chunkSize)
		}
	}
	_, _ = tb.Write([]byte("END"))
	if got := tb.String(); len(got) < JobLogKeepBytes || !strings.HasSuffix(got, "yEND") {
		t.Errorf("TailBuffer lost the end of the stream (len %d)", len(got))
	}
}
