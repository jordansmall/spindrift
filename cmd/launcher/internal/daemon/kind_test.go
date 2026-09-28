package daemon

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatchkind"
)

func TestKindOf_ChoreKeyed(t *testing.T) {
	for _, d := range dispatchkind.All {
		k := KindOf(d)
		if got, want := k.choreKeyed(), d.Keying == dispatchkind.ByChore; got != want {
			t.Errorf("KindOf(%s).choreKeyed() = %v, want %v", d.Name, got, want)
		}
	}
	if Kind("bogus").choreKeyed() {
		t.Error(`Kind("bogus").choreKeyed() = true, want false`)
	}
}

func TestParseKind(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    Kind
		wantErr bool
	}{
		{"empty defaults to dispatch", "", KindOf(dispatchkind.Work), false},
		{"dispatch", "dispatch", KindOf(dispatchkind.Work), false},
		{"research", "research", KindOf(dispatchkind.Research), false},
		{"butler", "butler", KindOf(dispatchkind.Butler), false},
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

func TestParseKinds(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []Kind
		wantErr bool
	}{
		{"empty defaults to every kind", "", []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research), KindOf(dispatchkind.Butler)}, false},
		{"dispatch", "dispatch", []Kind{KindOf(dispatchkind.Work)}, false},
		{"research", "research", []Kind{KindOf(dispatchkind.Research)}, false},
		{"butler", "butler", []Kind{KindOf(dispatchkind.Butler)}, false},
		{"unknown", "bogus", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseKinds(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseKinds(%q): want error, got nil", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseKinds(%q): unexpected error: %v", tc.in, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("ParseKinds(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("ParseKinds(%q) = %v, want %v", tc.in, got, tc.want)
				}
			}
		})
	}
}
