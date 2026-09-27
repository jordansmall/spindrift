package dispatchkind

import "testing"

func TestByName(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    *Descriptor
		wantErr bool
	}{
		{"work", "work", Work, false},
		{"research", "research", Research, false},
		{"unknown", "bogus", nil, true},
		{"empty is not special", "", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ByName(tc.in)
			if tc.wantErr {
				if ok {
					t.Fatalf("ByName(%q) = %v, true; want not found", tc.in, got)
				}
				return
			}
			if !ok {
				t.Fatalf("ByName(%q): not found", tc.in)
			}
			if got != tc.want {
				t.Fatalf("ByName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestByVerb(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    *Descriptor
		wantErr bool
	}{
		{"dispatch", "dispatch", Work, false},
		{"research", "research", Research, false},
		{"unknown", "bogus", nil, true},
		{"empty is not special", "", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ByVerb(tc.in)
			if tc.wantErr {
				if ok {
					t.Fatalf("ByVerb(%q) = %v, true; want not found", tc.in, got)
				}
				return
			}
			if !ok {
				t.Fatalf("ByVerb(%q): not found", tc.in)
			}
			if got != tc.want {
				t.Fatalf("ByVerb(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestAllOrder pins the daemon's default pool order (issue #3541): work
// before research.
func TestAllOrder(t *testing.T) {
	if len(All) != 2 || All[0] != Work || All[1] != Research {
		t.Fatalf("All = %v, want [Work, Research]", All)
	}
}

// TestUniqueAcrossAll guards against a future kind reusing a name or verb
// already claimed by another descriptor, which would make ByName/ByVerb
// ambiguous.
func TestUniqueAcrossAll(t *testing.T) {
	names := make(map[string]bool, len(All))
	verbs := make(map[string]bool, len(All))
	for _, d := range All {
		if names[d.Name] {
			t.Fatalf("duplicate name %q in All", d.Name)
		}
		names[d.Name] = true
		if verbs[d.Verb] {
			t.Fatalf("duplicate verb %q in All", d.Verb)
		}
		verbs[d.Verb] = true
	}
}
