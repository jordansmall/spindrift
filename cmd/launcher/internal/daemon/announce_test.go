package daemon

import "testing"

func TestParseAnnouncedIssue(t *testing.T) {
	cases := []struct {
		name      string
		line      string
		wantIssue string
		wantOK    bool
	}{
		{"run", "    -> #42: fix the thing\n", "42", true},
		{"fix pass", "    -> #42 (fix-pass-2): fix the thing\n", "42", true},
		{"conflict resolve", "    -> #42 (conflict-resolve): fix the thing\n", "42", true},
		{"no trailing newline", "    -> #42: fix the thing", "42", true},
		{"multi-digit", "    -> #123456: a title\n", "123456", true},
		{"heartbeat", "#123 [agent] doing a thing\n", "", false},
		{"completion", "    <- #123 done  (12m34s)\n", "", false},
		{"empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseAnnouncedIssue(tc.line)
			if ok != tc.wantOK || got != tc.wantIssue {
				t.Errorf("ParseAnnouncedIssue(%q) = (%q, %v), want (%q, %v)", tc.line, got, ok, tc.wantIssue, tc.wantOK)
			}
		})
	}
}
