package signalwire

import (
	"strings"
	"testing"
)

// TestIssueIntentValidate pins Validate's own rule set: required title and
// body, well-formed UTF-8 and size for every field actually set, and a
// legal class slug when Class is set. Type is deliberately not required
// here -- the socket enforces that on top (signalsocket_test.go's
// TestRejectEmpty); a settle log-carrier payload's Type can be blank.
func TestIssueIntentValidate(t *testing.T) {
	bad := string([]byte{0xff})
	big := strings.Repeat("x", MaxBodyBytes+1)

	cases := []struct {
		name       string
		intent     IssueIntent
		wantStatus string // "" means Validate must return nil
		wantCode   int
	}{
		{"valid minimal", IssueIntent{Title: "t", Body: "b"}, "", 0},
		{
			"valid with class, concurrence, dedup",
			IssueIntent{Title: "t", Body: "b", Type: "bug", Class: "flaky-test", Concurrence: "confirmed", DedupTerms: []string{"term"}},
			"", 0,
		},
		{"blank dedup term ok", IssueIntent{Title: "t", Body: "b", DedupTerms: []string{"   "}}, "", 0},
		{"40-char class ok", IssueIntent{Title: "t", Body: "b", Class: strings.Repeat("a", MaxClassLen)}, "", 0},
		{"blank title", IssueIntent{Title: " ", Body: "b"}, "empty", 400},
		{"blank body", IssueIntent{Title: "t", Body: ""}, "empty", 400},
		{"invalid utf-8 in title", IssueIntent{Title: bad, Body: "b"}, "invalid_utf8", 400},
		{"invalid utf-8 in body", IssueIntent{Title: "t", Body: bad}, "invalid_utf8", 400},
		{"invalid utf-8 in type", IssueIntent{Title: "t", Body: "b", Type: bad}, "invalid_utf8", 400},
		{"invalid utf-8 in dedup term", IssueIntent{Title: "t", Body: "b", DedupTerms: []string{bad}}, "invalid_utf8", 400},
		{"invalid utf-8 in class", IssueIntent{Title: "t", Body: "b", Class: bad}, "invalid_utf8", 400},
		{"invalid utf-8 in concurrence", IssueIntent{Title: "t", Body: "b", Concurrence: bad}, "invalid_utf8", 400},
		{"oversize body", IssueIntent{Title: "t", Body: big}, "oversize", 413},
		{"oversize class", IssueIntent{Title: "t", Body: "b", Class: big}, "oversize", 413},
		{"bad class uppercase/underscore", IssueIntent{Title: "t", Body: "b", Class: "Error_Handling"}, "invalid_class", 400},
		{"bad class leading hyphen", IssueIntent{Title: "t", Body: "b", Class: "-x"}, "invalid_class", 400},
		{"bad class over max length", IssueIntent{Title: "t", Body: "b", Class: strings.Repeat("a", MaxClassLen+1)}, "invalid_class", 400},
		{"bad class embedded space", IssueIntent{Title: "t", Body: "b", Class: "a b"}, "invalid_class", 400},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rej := tc.intent.Validate()
			if tc.wantStatus == "" {
				if rej != nil {
					t.Fatalf("Validate() = %+v, want accept", *rej)
				}
				return
			}
			if rej == nil {
				t.Fatalf("Validate() = accept, want reject %q", tc.wantStatus)
			}
			if rej.Status != tc.wantStatus || rej.Code != tc.wantCode {
				t.Fatalf("Validate() = (%q, %d), want (%q, %d): %q", rej.Status, rej.Code, tc.wantStatus, tc.wantCode, rej.Reason)
			}
		})
	}
}

// TestIssueIntentValidateInvalidClassNamesRule pins invalid_class's Reason
// to ClassRule, so a Box learns the grammar it violated.
func TestIssueIntentValidateInvalidClassNamesRule(t *testing.T) {
	rej := IssueIntent{Title: "t", Body: "b", Class: "Not_A_Slug"}.Validate()
	if rej == nil {
		t.Fatal("Validate() = accept, want invalid_class reject")
	}
	if !strings.Contains(rej.Reason, ClassRule) {
		t.Errorf("Reason = %q, want it to contain ClassRule %q", rej.Reason, ClassRule)
	}
}
