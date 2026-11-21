package runner

import (
	"strings"
	"testing"
)

func TestBoundedWriter_KeepsOnlyTheTailUpToTheCap(t *testing.T) {
	var w BoundedWriter
	chunk := strings.Repeat("x", boundedOutputCap/2)
	for _, p := range []string{"START-MARKER", chunk, chunk, "END-MARKER"} {
		if n, err := w.Write([]byte(p)); n != len(p) || err != nil {
			t.Fatalf("Write(%d bytes) = %d, %v", len(p), n, err)
		}
	}
	got := w.String()
	if len(got) != boundedOutputCap {
		t.Errorf("len(String()) = %d, want %d", len(got), boundedOutputCap)
	}
	if !strings.HasSuffix(got, "END-MARKER") {
		t.Errorf("tail lost: want it to end with END-MARKER")
	}
	if strings.Contains(got, "START-MARKER") {
		t.Errorf("head retained: want START-MARKER dropped")
	}
}
