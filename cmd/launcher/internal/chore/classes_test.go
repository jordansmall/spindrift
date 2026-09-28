package chore

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseClasses(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    map[string][]string
		wantErr string // substring; "" means no error
	}{
		{"empty string", "", map[string][]string{}, ""},
		{
			"schema default", "bugs=error-handling,resource-leak refactor=dead-code docs-drift=stale-reference",
			map[string][]string{
				"bugs":       {"error-handling", "resource-leak"},
				"refactor":   {"dead-code"},
				"docs-drift": {"stale-reference"},
			},
			"",
		},
		{"single entry single class", "bugs=error-handling", map[string][]string{"bugs": {"error-handling"}}, ""},
		{"missing equals", "bugs", nil, "missing '='"},
		{"invalid chore name", "bugs/x=error-handling", nil, "invalid chore name"},
		{"invalid class name", "bugs=error@handling", nil, "invalid class"},
		{"empty class list", "bugs=", nil, "invalid class"},
		{"trailing comma empty class", "bugs=error-handling,", nil, "invalid class"},
		{"duplicate chore", "bugs=a bugs=b", nil, "duplicate chore"},
		{"duplicate class within entry", "bugs=a,a", nil, "duplicate class"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseClasses(tc.in)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ParseClasses(%q): %v", tc.in, err)
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("ParseClasses(%q) = %#v, want %#v", tc.in, got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("ParseClasses(%q): got nil error, want substring %q", tc.in, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ParseClasses(%q) err = %v, want substring %q", tc.in, err, tc.wantErr)
			}
		})
	}
}
