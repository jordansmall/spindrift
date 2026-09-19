package daemon

import "testing"

func TestParseKind(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    Kind
		wantErr bool
	}{
		{"empty defaults to dispatch", "", KindDispatch, false},
		{"dispatch", "dispatch", KindDispatch, false},
		{"research", "research", KindResearch, false},
		{"unknown", "bogus", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseKind(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseKind(%q): want error, got nil", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseKind(%q): unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ParseKind(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
