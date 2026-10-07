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

// TestRelayCapableAndHostPostingCapable pins the two read-only capability bits
// (issue #2526) that mkHarness's eval assert reads straight off the registry
// rows: RelayCapable on the forge axis and HostPostingCapable on the tracker
// axis.
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

// TestLabelRegistry pins which trackers manage labels as registered objects:
// doctor's extra Required labels would deadlock a preflight on one that does
// not (issue #4400).
func TestLabelRegistry(t *testing.T) {
	for _, tc := range []struct {
		name string
		desc Descriptor
		want bool
	}{
		{"github", GitHub, true},
		{"forgejo", Forgejo, true},
		{"local", Local, false},
		{"jira", Jira, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.desc.LabelRegistry; got != tc.want {
				t.Fatalf("%s.LabelRegistry = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestNeedsGHToken(t *testing.T) {
	cases := []struct {
		name           string
		forge, tracker Descriptor
		want           bool
	}{
		{"github/github", GitHub, GitHub, true},
		{"forgejo/forgejo", Forgejo, Forgejo, false},
		{"jira tracker, github forge", GitHub, Jira, true},
		{"forgejo tracker, github forge", GitHub, Forgejo, true},
		{"github tracker, forgejo forge", Forgejo, GitHub, true},
		{"jira tracker, forgejo forge", Forgejo, Jira, false},
		{"git forge, forgejo tracker", Git, Forgejo, true},
		{"git forge, jira tracker", Git, Jira, true},
		{"local forge, forgejo tracker", Local, Forgejo, false},
		{"local forge, jira tracker", Local, Jira, false},
		{"local forge, github tracker", Local, GitHub, true},
		{"unknown forge", Descriptor{}, Forgejo, true},
	}
	for _, c := range cases {
		if got := NeedsGHToken(c.forge, c.tracker); got != c.want {
			t.Errorf("%s: NeedsGHToken(%q, %q) = %v, want %v", c.name, c.forge.Name, c.tracker.Name, got, c.want)
		}
	}
}

// TestInBoxOpenPRQueryable pins the exact set of CODE_FORGE backends whose open
// PRs the Box can query itself (see Descriptor.InBoxOpenPRQueryable).
func TestInBoxOpenPRQueryable(t *testing.T) {
	var got []string
	for _, d := range Registry {
		if d.InBoxOpenPRQueryable {
			got = append(got, d.Name)
		}
	}
	if want := []string{"github"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("InBoxOpenPRQueryable set = %v, want %v", got, want)
	}
}

// TestInBoxGHCredentialHelper pins the exact set of CODE_FORGE backends whose
// Box clone runs `gh auth setup-git` (see Descriptor.InBoxGHCredentialHelper).
func TestInBoxGHCredentialHelper(t *testing.T) {
	var got []string
	for _, d := range Registry {
		if d.InBoxGHCredentialHelper {
			got = append(got, d.Name)
		}
	}
	if want := []string{"github", "git"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("InBoxGHCredentialHelper set = %v, want %v", got, want)
	}
}

// TestDescriptorFieldsMatchNixRows pins the hand-written Descriptor struct to
// the field table lib/renderers.nix renders registry_gen.go from, so a field
// added on only one side fails here instead of silently never being set.
func TestDescriptorFieldsMatchNixRows(t *testing.T) {
	structFields := map[string]bool{}
	typ := reflect.TypeOf(Descriptor{})
	for i := 0; i < typ.NumField(); i++ {
		structFields[typ.Field(i).Name] = true
	}
	nixFields := map[string]bool{}
	for _, name := range descriptorRowFields {
		nixFields[name] = true
	}
	for name := range structFields {
		if !nixFields[name] {
			t.Errorf("Descriptor.%s has no entry in rowFields (lib/renderers.nix); add the row attribute there and set it on the lib/backends/default.nix rows that need a non-default value, then run `nix run .#regen`", name)
		}
	}
	for name := range nixFields {
		if !structFields[name] {
			t.Errorf("rowFields (lib/renderers.nix) renders %s but Descriptor has no such field; add it to the struct in registry.go or drop the rowFields entry, then run `nix run .#regen`", name)
		}
	}
}

func TestTrackerAxisAndForgeBackendSignals(t *testing.T) {
	cases := []struct {
		name               string
		read, write, filer string
		forge              string
	}{
		{"github", "GITHUB", "GITHUB", "GH", "GH"},
		{"local", "LOCAL", "", "GH", "GH"},
		{"forgejo", "FORGEJO", "FORGEJO", "FORGEJO", "FORGEJO"},
		{"jira", "GITHUB", "GITHUB", "GH", "GH"},
		// Unregistered: ByName returns a zero-value Descriptor, a branch
		// distinct from a registered row with unset fields (issue #2533 review).
		{"not-a-real-backend", "GITHUB", "GITHUB", "GH", "GH"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			read, write, filer := TrackerAxisSignals(tc.name)
			if read != tc.read || write != tc.write || filer != tc.filer {
				t.Errorf("TrackerAxisSignals(%q) = (%q,%q,%q), want (%q,%q,%q)", tc.name, read, write, filer, tc.read, tc.write, tc.filer)
			}
			if got := ForgeBackendSignal(tc.name); got != tc.forge {
				t.Errorf("ForgeBackendSignal(%q) = %q, want %q", tc.name, got, tc.forge)
			}
		})
	}
}

// TestRelaysOutbox pins the one predicate for "a read-only Box's outbox
// bundle gets relayed" (issue #4656): an OutboxRelayCapable CODE_FORGE under
// read-only access, and nothing else.
func TestRelaysOutbox(t *testing.T) {
	cases := []struct {
		codeForge, boxAccess string
		want                 bool
	}{
		{"github", "read-only", true},
		{"forgejo", "read-only", true},
		{"github", "read-write", false},
		{"forgejo", "read-write", false},
		{"local", "read-only", false},
		{"git", "read-only", false},
		{"nope", "read-only", false},
		{"", "read-only", false},
		{"github", "", false},
	}
	for _, tc := range cases {
		if got := RelaysOutbox(tc.codeForge, tc.boxAccess); got != tc.want {
			t.Errorf("RelaysOutbox(%q, %q) = %v, want %v", tc.codeForge, tc.boxAccess, got, tc.want)
		}
	}
}
