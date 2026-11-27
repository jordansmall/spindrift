package forgejo

import "testing"

// An unset or unrecognized method falls back to "rebase", matching the github
// adapter's default.
func TestMergeStyle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method string
		want   string
	}{
		{"merge", "merge"},
		{"squash", "squash"},
		{"rebase", "rebase"},
		{"", "rebase"},
		{"bogus", "rebase"},
	}
	for _, tt := range tests {
		if got := MergeStyle(tt.method); got != tt.want {
			t.Errorf("MergeStyle(%q) = %q, want %q", tt.method, got, tt.want)
		}
	}
}
