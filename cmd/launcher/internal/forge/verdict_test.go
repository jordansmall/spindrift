package forge_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

func TestParseVerdict(t *testing.T) {
	cases := []struct {
		status string
		want   forge.Verdict
		wantOK bool
	}{
		{"recommend", forge.Recommend, true},
		{"reject", forge.Reject, true},
		{"unclear", forge.Unclear, true},
		{"blocked", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := forge.ParseVerdict(c.status)
		if ok != c.wantOK {
			t.Errorf("ParseVerdict(%q) ok = %v, want %v", c.status, ok, c.wantOK)
			continue
		}
		if ok && got != c.want {
			t.Errorf("ParseVerdict(%q) = %v, want %v", c.status, got, c.want)
		}
	}
}
