package promptfence

import (
	"strings"
	"testing"
)

// TestBlock mirrors orchestrator/run_test.go's TestFenceBlock (issue #2550
// review finding): moved alongside promptfence.Block per issue #3445.
func TestBlock(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantFence string
	}{
		{"no backticks", "plain text", "```"},
		{"three backticks", "some ```code``` here", "````"},
		{"four backticks", "some ````code```` here", "`````"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Block(tt.content)
			if !strings.HasPrefix(got, tt.wantFence+"\n") {
				t.Errorf("Block(%q) = %q, want to start with fence %q", tt.content, got, tt.wantFence)
			}
			if !strings.HasSuffix(got, "\n"+tt.wantFence) {
				t.Errorf("Block(%q) = %q, want to end with fence %q", tt.content, got, tt.wantFence)
			}
			if !strings.Contains(got, tt.content) {
				t.Errorf("Block(%q) = %q, want content present verbatim", tt.content, got)
			}
		})
	}
}
