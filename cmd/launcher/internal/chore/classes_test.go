package chore

import (
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/signalwire"
)

func TestParseClasses(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		want     map[string][]string
		wantErr  string // substring; "" means no error
		wantRule bool   // wantErr's error must also contain signalwire.ClassRule
	}{
		{"empty string", "", map[string][]string{}, "", false},
		{
			"schema default", "bugs=error-handling,resource-leak refactor=dead-code docs-drift=stale-reference",
			map[string][]string{
				"bugs":       {"error-handling", "resource-leak"},
				"refactor":   {"dead-code"},
				"docs-drift": {"stale-reference"},
			},
			"", false,
		},
		{"single entry single class", "bugs=error-handling", map[string][]string{"bugs": {"error-handling"}}, "", false},
		{"missing equals", "bugs", nil, "missing '='", false},
		{"invalid chore name", "bugs/x=error-handling", nil, "invalid chore name", false},
		{"invalid class name", "bugs=error@handling", nil, "invalid class", true},
		{"empty class list", "bugs=", nil, "invalid class", true},
		{"trailing comma empty class", "bugs=error-handling,", nil, "invalid class", true},
		{"duplicate chore", "bugs=a bugs=b", nil, "duplicate chore", false},
		{"duplicate class within entry", "bugs=a,a", nil, "duplicate class", false},
		{"uppercase class rejected", "bugs=Error_Handling", nil, `invalid class "Error_Handling"`, true},
		{"underscore class rejected", "bugs=error_handling", nil, `invalid class "error_handling"`, true},
		{"leading hyphen class rejected", "bugs=-leading", nil, `invalid class "-leading"`, true},
		{
			"class over max length rejected", "bugs=" + strings.Repeat("a", signalwire.MaxClassLen+1), nil,
			`invalid class "` + strings.Repeat("a", signalwire.MaxClassLen+1) + `"`, true,
		},
		{
			"class at max length accepted", "bugs=" + strings.Repeat("a", signalwire.MaxClassLen),
			map[string][]string{"bugs": {strings.Repeat("a", signalwire.MaxClassLen)}}, "", false,
		},
		{"digit-leading class accepted", "bugs=4xx", map[string][]string{"bugs": {"4xx"}}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseClasses(tc.in)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("parseClasses(%q): %v", tc.in, err)
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("parseClasses(%q) = %#v, want %#v", tc.in, got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("parseClasses(%q): got nil error, want substring %q", tc.in, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("parseClasses(%q) err = %v, want substring %q", tc.in, err, tc.wantErr)
			}
			if tc.wantRule && !strings.Contains(err.Error(), signalwire.ClassRule) {
				t.Errorf("parseClasses(%q) err = %v, want it to contain signalwire.ClassRule %q", tc.in, err, signalwire.ClassRule)
			}
		})
	}
}
