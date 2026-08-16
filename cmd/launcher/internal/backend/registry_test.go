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

// TestRelayCapableAndHostPostingCapable pins the two read-only capability
// bits (issue #2526) that mkHarness's eval assert (slice 2) will read
// straight off the registry rows: RelayCapable (forge axis -- bundle-relay
// always, plus draft-PR-create + commit-subjects when the backend has a PR
// concept) and HostPostingCapable (tracker axis -- host-posted comments +
// issue-filing).
func TestRelayCapableAndHostPostingCapable(t *testing.T) {
	relayCases := []struct {
		name string
		desc Descriptor
		want bool
	}{
		{"github", GitHub, true},
		{"forgejo", Forgejo, true},
		{"local", Local, true},
		{"git", Git, false},
	}
	for _, tc := range relayCases {
		t.Run("RelayCapable/"+tc.name, func(t *testing.T) {
			if got := tc.desc.RelayCapable; got != tc.want {
				t.Fatalf("%s.RelayCapable = %v, want %v", tc.name, got, tc.want)
			}
		})
	}

	hostPostingCases := []struct {
		name string
		desc Descriptor
		want bool
	}{
		{"github", GitHub, true},
		{"forgejo", Forgejo, true},
		{"local", Local, true},
		{"jira", Jira, false},
	}
	for _, tc := range hostPostingCases {
		t.Run("HostPostingCapable/"+tc.name, func(t *testing.T) {
			if got := tc.desc.HostPostingCapable; got != tc.want {
				t.Fatalf("%s.HostPostingCapable = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
