package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResultEventRoundTrip covers every registered Driver, so a new Driver's
// ResultEvent is checked against its own ResultText without a new test.
func TestResultEventRoundTrip(t *testing.T) {
	text := "line one\n\"quoted\" \\ back\nSPINDRIFT_OUTCOME status=blocked"
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			d, err := New(name)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			ev, err := d.ResultEvent(text)
			if err != nil {
				t.Fatalf("ResultEvent: %v", err)
			}
			if !strings.HasSuffix(string(ev), "\n") || strings.Count(string(ev), "\n") != 1 {
				t.Fatalf("ResultEvent = %q, want exactly one trailing newline", ev)
			}
			path := filepath.Join(t.TempDir(), "stream.log")
			if err := os.WriteFile(path, ev, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := d.ResultText(path)
			if err != nil {
				t.Fatalf("ResultText: %v", err)
			}
			if got != text+"\n" {
				t.Errorf("ResultText = %q, want %q (one value per line)", got, text+"\n")
			}
		})
	}
}
