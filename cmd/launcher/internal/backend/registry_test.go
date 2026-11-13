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
			if got != tc.want {
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
		{"git forge, forgejo tracker", Git, Forgejo, true},
		{"git forge, jira tracker", Git, Jira, true},
		{"local forge, forgejo tracker", Local, Forgejo, false},
		{"local forge, jira tracker", Local, Jira, false},
		{"local forge, github tracker", Local, GitHub, true},
		{"unknown forge", Descriptor{}, Forgejo, true},
	}
	for _, c := range cases {
		if got := NeedsGHToken(c.forge, c.tracker, false); got != c.want {
			t.Errorf("%s: NeedsGHToken(%q, %q) = %v, want %v", c.name, c.forge.Name, c.tracker.Name, got, c.want)
		}
	}
}

// Under read-only a git Box clones the host-seeded mirror, so its gh credential
// helper never runs and only a GitHub tracker or forge token keeps GH_TOKEN
// required (#6898).
func TestNeedsGHTokenReadOnlyMirror(t *testing.T) {
	cases := []struct {
		name           string
		forge, tracker Descriptor
		want           bool
	}{
		{"git forge, jira tracker", Git, Jira, false},
		{"git forge, forgejo tracker", Git, Forgejo, false},
		{"git forge, local tracker", Git, Local, false},
		{"git forge, github tracker", Git, GitHub, true},
		{"github forge, jira tracker", GitHub, Jira, true},
		{"unknown forge", Descriptor{}, Jira, true},
	}
	for _, c := range cases {
		if got := NeedsGHToken(c.forge, c.tracker, true); got != c.want {
			t.Errorf("%s: NeedsGHToken(%q, %q, readOnly) = %v, want %v", c.name, c.forge.Name, c.tracker.Name, got, c.want)
		}
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

func TestResearchVerdictRelayed(t *testing.T) {
	cases := []struct {
		tracker string
		filer   bool
		want    bool
	}{
		{"github", false, false},
		{"github", true, true},
		{"forgejo", false, false},
		{"local", false, false},
		{"jira", false, true},
		{"jira", true, true},
		{"", false, false},
		{"", true, true},
		{"nonesuch", false, false},
	}
	for _, tc := range cases {
		if got := ResearchVerdictRelayed(tc.tracker, tc.filer); got != tc.want {
			t.Errorf("ResearchVerdictRelayed(%q, %v) = %v, want %v", tc.tracker, tc.filer, got, tc.want)
		}
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

func TestSlugIssueRefTrackerIsLocalOnly(t *testing.T) {
	for _, d := range Registry {
		want := d.Name == "local"
		if d.SlugIssueRefTracker != want {
			t.Errorf("%s.SlugIssueRefTracker = %v, want %v", d.Name, d.SlugIssueRefTracker, want)
		}
	}
}

func TestClosesRefTrackerIsGithubOnly(t *testing.T) {
	for _, name := range []string{"github", "forgejo", "jira", "local", "git"} {
		d, ok := ByName(name)
		if !ok {
			t.Fatalf("ByName(%q) not registered", name)
		}
		if want := name == "github"; d.ClosesRefTracker != want {
			t.Errorf("%s.ClosesRefTracker = %v, want %v", name, d.ClosesRefTracker, want)
		}
	}
}

// TestRelaysBundle pins the single "this Box hands its branch back as an
// outbox bundle" decision: a host-mediated remote always, a relay-capable
// forge only under a read-only Box.
func TestRelaysBundle(t *testing.T) {
	cases := []struct {
		name     string
		desc     Descriptor
		readOnly bool
		want     bool
	}{
		{"local read-write", Local, false, true},
		{"local read-only", Local, true, true},
		{"github read-write", GitHub, false, false},
		{"github read-only", GitHub, true, true},
		{"forgejo read-only", Forgejo, true, true},
		{"git read-write", Git, false, false},
		{"git read-only", Git, true, true},
		{"relay-incapable read-only", Descriptor{}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RelaysBundle(tc.desc, tc.readOnly); got != tc.want {
				t.Fatalf("RelaysBundle(%s, readOnly=%v) = %v, want %v", tc.name, tc.readOnly, got, tc.want)
			}
		})
	}
}

// TestClonesFromMirror pins when the Box clones from the host-seeded mirror:
// only a HostSeededMirror forge whose Box hands its branch back as a bundle.
func TestClonesFromMirror(t *testing.T) {
	cases := []struct {
		name     string
		desc     Descriptor
		readOnly bool
		want     bool
	}{
		{"local read-write", Local, false, true},
		{"local read-only", Local, true, true},
		{"git read-write", Git, false, false},
		{"git read-only", Git, true, true},
		{"github read-only", GitHub, true, false},
		{"forgejo read-only", Forgejo, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClonesFromMirror(tc.desc, tc.readOnly); got != tc.want {
				t.Fatalf("ClonesFromMirror(%s, readOnly=%v) = %v, want %v", tc.name, tc.readOnly, got, tc.want)
			}
		})
	}
}
