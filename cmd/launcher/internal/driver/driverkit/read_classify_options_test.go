package driverkit

import (
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/logscan"
)

func TestClassifyFirstMatchLatchesEarliestMarker(t *testing.T) {
	opts := ClassifyOptions{FirstMatch: true}
	path, nd := evLines(t, Event{Kind: KindError, Text: "overloaded"}, Event{Kind: KindError, Text: "rate_limit"})
	if got, _ := Classify(path, nd, rlExtras, termExtras, opts); got.Reason != Overloaded {
		t.Errorf("FirstMatch: got %+v, want the earlier Overloaded", got)
	}
	if got, _ := Classify(path, nd, rlExtras, termExtras, ClassifyOptions{}); got.Reason != RateLimit {
		t.Errorf("default: got %+v, want the later RateLimit", got)
	}
	path, nd = evLines(t, Event{Kind: KindError, Text: "overloaded"}, Event{Kind: KindText}, Event{Kind: KindError, Text: "rate_limit"})
	if got, _ := Classify(path, nd, rlExtras, termExtras, opts); got.Reason != RateLimit {
		t.Errorf("progress must release the FirstMatch latch: got %+v", got)
	}
}

func TestClassifyFirstMatchResetAtIsFirstSinceProgress(t *testing.T) {
	opts := ClassifyOptions{FirstMatch: true}
	first := time.Unix(1783192800, 0).UTC()
	later := first.Add(time.Hour)

	// The reset rides a later error that matches no marker.
	path, nd := evLines(t, Event{Kind: KindError, Text: "rate_limit"}, Event{Kind: KindError, Text: "bare", ResetAt: &first})
	got, _ := Classify(path, nd, rlExtras, termExtras, opts)
	if got.Reason != RateLimit || got.ResetAt == nil || !got.ResetAt.Equal(first) {
		t.Errorf("got %+v, want RateLimit resetting at %v", got, first)
	}
	if got, _ := Classify(path, nd, rlExtras, termExtras, ClassifyOptions{}); got.ResetAt != nil {
		t.Errorf("default must not take a non-matching error's reset, got %v", got.ResetAt)
	}

	// The first reset wins over a later one.
	path, nd = evLines(t, Event{Kind: KindError, Text: "rate_limit", ResetAt: &first}, Event{Kind: KindError, Text: "x", ResetAt: &later})
	if got, _ := Classify(path, nd, rlExtras, termExtras, opts); got.ResetAt == nil || !got.ResetAt.Equal(first) {
		t.Errorf("got %v, want first reset %v", got.ResetAt, first)
	}

	// Progress discards a reset gathered before it.
	path, nd = evLines(t, Event{Kind: KindError, Text: "x", ResetAt: &first}, Event{Kind: KindStart}, Event{Kind: KindError, Text: "rate_limit"})
	if got, _ := Classify(path, nd, rlExtras, termExtras, opts); got.Reason != RateLimit || got.ResetAt != nil {
		t.Errorf("got %+v, want RateLimit without the pre-progress reset", got)
	}
}

func TestClassifySyntheticTextIsNotProgress(t *testing.T) {
	path, nd := evLines(t, Event{Kind: KindError, Text: "rate_limit"}, Event{Kind: KindText, Synthetic: true})
	if got, _ := Classify(path, nd, rlExtras, termExtras, ClassifyOptions{}); got.Reason != RateLimit {
		t.Errorf("synthetic text recovered the error: %+v", got)
	}
	path, nd = evLines(t, Event{Kind: KindError, Text: "rate_limit"}, Event{Kind: KindText})
	if got, _ := Classify(path, nd, rlExtras, termExtras, ClassifyOptions{}); got.Reason != TaskFailed {
		t.Errorf("real text must recover the error: %+v", got)
	}
}

func TestClassifyOversizedPolicy(t *testing.T) {
	long := strings.Repeat("x", 5<<20) + " rate_limit"
	path := writeClassifyLog(t, long)
	nd := func() Decoder {
		return func(line string) []Event { return []Event{{Kind: KindError, Text: line}} }
	}
	if got, _ := Classify(path, nd, rlExtras, termExtras, ClassifyOptions{}); got.Reason != TaskFailed {
		t.Errorf("default skips the oversized line: %+v", got)
	}
	if got, _ := Classify(path, nd, rlExtras, termExtras, ClassifyOptions{Oversized: logscan.ChunkOversized}); got.Reason != RateLimit {
		t.Errorf("ChunkOversized must scan the line in chunks: %+v", got)
	}
}
