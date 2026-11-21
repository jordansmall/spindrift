package backend

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/testutil"
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
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ByName(%q) = %+v, want %+v", tc.name, got, tc.want)
			}
		})
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
		{"git", Git, true},
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
		{"jira", Jira, true},
	}
	for _, tc := range hostPostingCases {
		t.Run("HostPostingCapable/"+tc.name, func(t *testing.T) {
			if got := tc.desc.HostPostingCapable; got != tc.want {
				t.Fatalf("%s.HostPostingCapable = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestLabelRegistry pins which trackers manage labels as registered objects
// (doctor's extra Required labels would deadlock a preflight on one that does
// not, issue #4400) and which carry labels on their issues, registry or not:
// jira does, though it has no label registry (issue #6347).
func TestLabelRegistry(t *testing.T) {
	for _, tc := range []struct {
		name              string
		desc              Descriptor
		registry, wearing bool
	}{
		{"github", GitHub, true, true},
		{"forgejo", Forgejo, true, true},
		{"local", Local, false, false},
		{"jira", Jira, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.desc.LabelRegistry; got != tc.registry {
				t.Errorf("%s.LabelRegistry = %v, want %v", tc.name, got, tc.registry)
			}
			if got := tc.desc.IssuesWearLabels; got != tc.wearing {
				t.Errorf("%s.IssuesWearLabels = %v, want %v", tc.name, got, tc.wearing)
			}
		})
	}
	// A registered label the tracker's issues cannot wear means nothing, so no
	// row may set LabelRegistry without IssuesWearLabels.
	for _, d := range Registry {
		if d.LabelRegistry && !d.IssuesWearLabels {
			t.Errorf("%s sets LabelRegistry without IssuesWearLabels", d.Name)
		}
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
		{"git forge, forgejo tracker", Git, Forgejo, false},
		{"git forge, jira tracker", Git, Jira, false},
		{"git forge, github tracker", Git, GitHub, true},
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

func TestClosesRef(t *testing.T) {
	closing := map[[2]string]bool{
		{"github", "github"}:   true,
		{"forgejo", "forgejo"}: true,
	}
	rows := append([]Descriptor{{}}, Registry...)
	for _, forge := range rows {
		for _, tracker := range rows {
			want := closing[[2]string{forge.Name, tracker.Name}]
			if got := ClosesRef(forge, tracker); got != want {
				t.Errorf("ClosesRef(forge %q, tracker %q) = %v, want %v", forge.Name, tracker.Name, got, want)
			}
		}
	}
}

// TestDescriptorFieldsMatchNixRows pins the hand-written Descriptor struct to
// the field table lib/renderers.nix renders registry_gen.go from, so a field
// added on only one side fails here instead of silently never being set.
func TestDescriptorFieldsMatchNixRows(t *testing.T) {
	testutil.AssertStructFieldsMatchRows(t, reflect.TypeOf(Descriptor{}), descriptorRowFields,
		"lib/renderers.nix",
		"the lib/backends/<name>.nix rows that need a non-default value",
		"registry.go")
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
		{"jira", "JIRA", "GITHUB", "GH", "GH"},
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

// TestQuickstartBlocks pins the shipped rows' quickstart declarations; every
// block must name the tracker axis, since quickstart's answer is the tracker.
func TestQuickstartBlocks(t *testing.T) {
	want := map[string]QuickstartBlock{
		"github":  {Hosts: []string{"github.com"}, Axes: []string{"tracker", "forge"}},
		"forgejo": {Hosts: []string{"codeberg.org"}, Axes: []string{"tracker", "forge"}},
	}
	for _, d := range Registry {
		w, ok := want[d.Name]
		if !ok {
			if d.Quickstart != nil {
				t.Errorf("%s carries an unexpected quickstart block %+v", d.Name, *d.Quickstart)
			}
			continue
		}
		if d.Quickstart == nil || !reflect.DeepEqual(*d.Quickstart, w) {
			t.Errorf("%s quickstart = %+v, want %+v", d.Name, d.Quickstart, w)
		}
	}
}

// TestRouteKindsAreLocalLabelOnly pins ADR 0075 decision 1: only local
// declares an Issue route kind (label); every other row declares none, since a
// tracker that ignored the route would quietly list the whole Tracker scope.
func TestRouteKindsAreLocalLabelOnly(t *testing.T) {
	for _, d := range Registry {
		var want []string
		if d.Name == "local" {
			want = []string{"label"}
		}
		if !reflect.DeepEqual(d.RouteKinds, want) {
			t.Errorf("%s.RouteKinds = %v, want %v", d.Name, d.RouteKinds, want)
		}
	}
}

func TestSlugIssueRefTrackerIsLocalOnly(t *testing.T) {
	for _, d := range Registry {
		want := d.Name == "local"
		if d.SlugIssueRefTracker != want {
			t.Errorf("%s.SlugIssueRefTracker = %v, want %v", d.Name, d.SlugIssueRefTracker, want)
		}
	}
}

// TestRelaysOutbox pins the one predicate for "a Box's outbox bundle gets
// relayed into a PR" (issue #4656): an OutboxRelayCapable CODE_FORGE.
func TestRelaysOutbox(t *testing.T) {
	for codeForge, want := range map[string]bool{
		"github": true, "forgejo": true,
		"local": false, "git": false, "nope": false, "": false,
	} {
		if got := RelaysOutbox(codeForge); got != want {
			t.Errorf("RelaysOutbox(%q) = %v, want %v", codeForge, got, want)
		}
	}
}

// TestRelaysBundle pins the single "this Box hands its branch back as an
// outbox bundle" decision: a host-mediated remote or a relay-capable forge.
func TestRelaysBundle(t *testing.T) {
	for _, d := range []Descriptor{Local, GitHub, Forgejo, Git} {
		if !RelaysBundle(d) {
			t.Errorf("RelaysBundle(%s) = false, want true", d.Name)
		}
	}
	if RelaysBundle(Descriptor{}) {
		t.Error("RelaysBundle of a relay-incapable forge = true, want false")
	}
}
