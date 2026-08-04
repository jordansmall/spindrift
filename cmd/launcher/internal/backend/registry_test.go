package backend

import (
	"reflect"
	"testing"
)

func TestByName(t *testing.T) {
	cases := []struct {
		name string
		want Descriptor
		ok   bool
	}{
		{"github", GitHub, true},
		{"forgejo", Forgejo, true},
		{"jira", Jira, true},
		{"local", Local, true},
		{"git", Git, true},
		{"nope", Descriptor{}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ByName(tc.name)
			if ok != tc.ok {
				t.Fatalf("ByName(%q) ok = %v, want %v", tc.name, ok, tc.ok)
			}
			if got != tc.want {
				t.Fatalf("ByName(%q) = %+v, want %+v", tc.name, got, tc.want)
			}
		})
	}
}

func TestQuickstartEligible(t *testing.T) {
	got := QuickstartEligible()
	want := []Descriptor{GitHub, Forgejo}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("QuickstartEligible() = %+v, want %+v", got, want)
	}
}
