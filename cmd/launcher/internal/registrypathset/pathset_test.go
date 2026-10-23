package registrypathset

import (
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest/synthetic"
	"spindrift.dev/launcher/internal/registryvocab"
	"spindrift.dev/launcher/internal/testutil"
)

// registryvocab.PathSet.Admits owns the admission rule (issue #3398), so this
// helper projects Subtrees down to their roots and delegates rather than
// reimplementing the rule here.
func (s HostPathSet) admits(requestPath string) bool {
	roots := make(registryvocab.PathSet, len(s.Subtrees))
	for i, sub := range s.Subtrees {
		roots[i] = sub.Path
	}
	return roots.Admits(requestPath)
}

var syntheticRows = []ecosystem.Strategy{synthetic.Row}

// deriveSynthetic writes body to the synthetic row's config path in a fresh dir
// and derives over that row alone.
func deriveSynthetic(t *testing.T, body string) []HostPathSet {
	t.Helper()
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, synthetic.Row.InTreeConfigPath, body)
	got, err := DeriveRows(dir, syntheticRows)
	if err != nil {
		t.Fatalf("DeriveRows: unexpected error: %v", err)
	}
	return got
}

// Discover dedupes by host and drops the second registry. Derive must not: a
// repo declaring two registries on one host needs both subtrees, or the
// enforced path set refuses what the second registry serves.
func TestDerive_TwoRegistriesOneHost(t *testing.T) {
	got := deriveSynthetic(t, `
internal = https://artifacts.example.com/artifactory/api/internal/index
remote = https://artifacts.example.com/artifactory/api/remote/index
`)
	want := []HostPathSet{{
		Host:   "artifacts.example.com",
		Origin: "https://artifacts.example.com",
		Subtrees: []registryvocab.Subtree{
			{Ecosystem: synthetic.Row.Name, Path: "/artifactory/api/internal/index", RegistryName: "internal"},
			{Ecosystem: synthetic.Row.Name, Path: "/artifactory/api/remote/index", RegistryName: "remote"},
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DeriveRows = %+v, want %+v", got, want)
	}
}

// Two declarations disagreeing on scheme and port still merge into one
// HostPathSet, because enforcement keys on the registryvocab.HostKey-normalized
// host and a second entry for that host would be unreachable. The first
// declaration's https Origin is the one kept.
func TestDerive_SameHostOriginDisagreementMerges(t *testing.T) {
	got := deriveSynthetic(t, "a = https://host.example.com/one\nb = http://host.example.com:8080/two\n")
	want := []HostPathSet{{
		Host:   "host.example.com",
		Origin: "https://host.example.com",
		Subtrees: []registryvocab.Subtree{
			{Ecosystem: synthetic.Row.Name, Path: "/one", RegistryName: "a"},
			{Ecosystem: synthetic.Row.Name, Path: "/two", RegistryName: "b"},
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DeriveRows = %+v, want %+v", got, want)
	}
}

// The two hosts differ only in case deliberately: equivalent hosts but distinct
// strings, so a row's byte-equal dedupe would pass both through and Derive's
// own dedupe, over the registryvocab.HostKey-folded host, is what must collapse
// them.
func TestDerive_ExactRepeatWithinHostDedupes(t *testing.T) {
	got := deriveSynthetic(t, "a = https://x.example.com/repo\nb = https://X.example.com/repo\n")
	want := []HostPathSet{{
		Host:     "x.example.com",
		Origin:   "https://x.example.com",
		Subtrees: []registryvocab.Subtree{{Ecosystem: synthetic.Row.Name, Path: "/repo", RegistryName: "a"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DeriveRows = %+v, want %+v", got, want)
	}
}

// Only a port-less IPv6 literal reaches registryvocab.HostKey's bracket branch.
// Host unbrackets to stay comparable with a route's match-host, while Origin
// keeps the brackets because that is the authority a client dials.
func TestDerive_IPv6HostUnbracketsButOriginKeepsBrackets(t *testing.T) {
	got := deriveSynthetic(t, "a = http://[::1]/repo\n")
	want := []HostPathSet{{
		Host:     "::1",
		Origin:   "http://[::1]",
		Subtrees: []registryvocab.Subtree{{Ecosystem: synthetic.Row.Name, Path: "/repo", RegistryName: "a"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DeriveRows = %+v, want %+v", got, want)
	}
}

// Absence of declaration means absence of binding: neither an empty repo nor
// one declaring only another row may derive a path for the synthetic row,
// because nothing in the snapshot names a registry for it.
func TestDerive_NoDeclarationDerivesNoPaths(t *testing.T) {
	t.Run("empty repo", func(t *testing.T) {
		got, err := DeriveRows(t.TempDir(), syntheticRows)
		if err != nil {
			t.Fatalf("DeriveRows: unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("DeriveRows = %+v, want no host path sets", got)
		}
	})

	t.Run("other row declared but not this one", func(t *testing.T) {
		twin := synthetic.Twin("synthetic-twin")
		dir := t.TempDir()
		testutil.WriteFixture(t, dir, twin.InTreeConfigPath, "a = https://twin.example.com/index\n")

		got, err := DeriveRows(dir, syntheticRows)
		if err != nil {
			t.Fatalf("DeriveRows: unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("DeriveRows over the synthetic row = %+v, want no host path sets: only the twin declares", got)
		}

		got, err = DeriveRows(dir, []ecosystem.Strategy{twin})
		if err != nil {
			t.Fatalf("DeriveRows: unexpected error: %v", err)
		}
		want := []HostPathSet{{
			Host:     "twin.example.com",
			Origin:   "https://twin.example.com",
			Subtrees: []registryvocab.Subtree{{Ecosystem: twin.Name, Path: "/index", RegistryName: "a"}},
		}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("DeriveRows over the twin = %+v, want %+v", got, want)
		}
	})
}

// A registry declared with no path at all is a whole-host registry, so it
// derives the root subtree rather than nothing.
func TestDerive_BareHostDerivesRootSubtree(t *testing.T) {
	got := deriveSynthetic(t, "a = https://registry.example.com\n")
	want := []HostPathSet{{
		Host:     "registry.example.com",
		Origin:   "https://registry.example.com",
		Subtrees: []registryvocab.Subtree{{Ecosystem: synthetic.Row.Name, Path: "/", RegistryName: "a"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DeriveRows = %+v, want %+v", got, want)
	}
}

// Host is registryvocab.HostKey-normalized so it compares equal to a route's
// match-host, while Origin keeps the port (and the case url.Parse preserves) so
// it stays a usable upstream origin.
func TestDerive_HostNormalizedOriginKeepsPort(t *testing.T) {
	got := deriveSynthetic(t, "a = https://HOST.example.com:8443/repo/index\n")
	want := []HostPathSet{{
		Host:     "host.example.com",
		Origin:   "https://HOST.example.com:8443",
		Subtrees: []registryvocab.Subtree{{Ecosystem: synthetic.Row.Name, Path: "/repo/index", RegistryName: "a"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DeriveRows = %+v, want %+v", got, want)
	}
}

// This test is acceptance criterion 3, over the shape the 2026-09-04 field run
// hit: an internal and a remote registry on one Artifactory host. The derived
// set must admit that run's real request paths and refuse the host's other API
// endpoints. A real-row twin of this shape lives in the ecosystem corpus
// (replaced-source-two-registries), which also covers a source replacement.
func TestDerive_TwoRegistriesOneHostAdmitsOnlyDeclaredSubtrees(t *testing.T) {
	got := deriveSynthetic(t, `
internal = https://artifacts.example.com/artifactory/api/internal/index
remote = https://artifacts.example.com/artifactory/api/remote/index
`)
	if len(got) != 1 {
		t.Fatalf("DeriveRows = %+v, want exactly one host path set", got)
	}
	set := got[0]
	if set.Host != "artifacts.example.com" {
		t.Fatalf("Host = %q, want %q", set.Host, "artifacts.example.com")
	}

	admit := []string{
		"/artifactory/api/internal/index",
		"/artifactory/api/internal/index/config.json",
		"/artifactory/api/internal/index/ax/um/axum",
		"/artifactory/api/remote/index",
		"/artifactory/api/remote/index/config.json",
		"/artifactory/api/remote/index/ax/um/axum",
	}
	for _, p := range admit {
		if !set.admits(p) {
			t.Errorf("Admits(%q) = false, want true: a real field-run request path", p)
		}
	}

	refuse := []string{
		// Credential-minting and administrative endpoints on the same host. No
		// declaration names them, so the operator credential must never be
		// steerable at them.
		"/artifactory/api/security/token",
		"/artifactory/api/repositories",
		"/artifactory/api/remote/index/../../security/token",
		// A download endpoint that is a sibling of the index, not under it, is
		// something nothing committed in the repo declares, so it is refused
		// today. Spec #3253 decision 3 has the Forwarder learn the download
		// base from upstream in a later ticket. This derivation must not
		// invent that path.
		"/artifactory/api/remote/v1/pkgs/axum/0.7.5/download",
	}
	for _, p := range refuse {
		if set.admits(p) {
			t.Errorf("Admits(%q) = true, want false: not a declared subtree", p)
		}
	}
}

// ecosystem.TestConfigParserMatchesInTreeConfigPath proves an in-tree row has a
// parser; this one proves a row's declaration reaches a subtree. A row whose
// config parses but derives no path would leave the proxy refusing every
// request that row makes, with no other test failing. The ecosystem corpus
// proves the same for each ecosystem.Table row.
func TestDerive_DeclarationReachesAdmittingSubtree(t *testing.T) {
	got := deriveSynthetic(t, "a = https://registry.example.com/repo/index\n")
	found := false
	for _, hps := range got {
		for _, s := range hps.Subtrees {
			if s.Ecosystem != synthetic.Row.Name {
				continue
			}
			found = true
			if !hps.admits(s.Path + "/config.json") {
				t.Errorf("derived subtree %q does not admit a path under itself", s.Path)
			}
		}
	}
	if !found {
		t.Errorf("DeriveRows = %+v, want a %s subtree", got, synthetic.Row.Name)
	}
}

// The snapshot directory alone is the input, so two calls over one unchanged
// fixture must agree exactly. Map iteration order in the grouping would break
// this.
func TestDerive_DeterministicOverSnapshot(t *testing.T) {
	twin := synthetic.Twin("synthetic-twin")
	rows := []ecosystem.Strategy{synthetic.Row, twin}
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, synthetic.Row.InTreeConfigPath, `
zeta = https://artifacts.example.com/artifactory/api/zeta/index
alpha = https://artifacts.example.com/artifactory/api/alpha/index
a = https://one.example.com/repo
b = https://other.example.com/repo
`)
	testutil.WriteFixture(t, dir, twin.InTreeConfigPath, "a = https://twin.example.com/repo\n")

	first, err := DeriveRows(dir, rows)
	if err != nil {
		t.Fatalf("DeriveRows: unexpected error: %v", err)
	}
	second, err := DeriveRows(dir, rows)
	if err != nil {
		t.Fatalf("DeriveRows: unexpected error: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("DeriveRows twice over one snapshot disagreed:\nfirst  = %+v\nsecond = %+v", first, second)
	}
}

// scopedRow reads `name url [scoped]` lines so a test can declare a scoped
// registry, which the synthetic row's parser never does.
var scopedRow = ecosystem.Strategy{
	Name:             "scopedfake",
	InTreeConfigPath: ".scopedfake/registries",
	ConfigParser: func(content string) ([]ecosystem.Declaration, bool, error) {
		var decls []ecosystem.Declaration
		for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
			f := strings.Fields(line)
			decls = append(decls, ecosystem.Declaration{
				Host:            strings.SplitN(strings.TrimPrefix(f[1], "https://"), "/", 2)[0],
				UpstreamBaseURL: f[1],
				RegistryName:    f[0],
				Scoped:          len(f) > 2 && f[2] == "scoped",
			})
		}
		return decls, true, nil
	},
}

func deriveScoped(t *testing.T, body string) []HostPathSet {
	t.Helper()
	dir := t.TempDir()
	testutil.WriteFixture(t, dir, scopedRow.InTreeConfigPath, body)
	got, err := DeriveRows(dir, []ecosystem.Strategy{scopedRow})
	if err != nil {
		t.Fatalf("DeriveRows: unexpected error: %v", err)
	}
	return got
}

func TestDerive_CopiesScopedFromDeclaration(t *testing.T) {
	got := deriveScoped(t, "a https://h.example.com/scoped scoped\nb https://h.example.com/plain\n")
	want := []registryvocab.Subtree{
		{Ecosystem: "scopedfake", Path: "/scoped", RegistryName: "a", Scoped: true},
		{Ecosystem: "scopedfake", Path: "/plain", RegistryName: "b"},
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0].Subtrees, want) {
		t.Errorf("DeriveRows = %+v, want one host with subtrees %+v", got, want)
	}
}

// A path declared both scoped and unscoped must stay bound: only a path every
// declaration scopes is skipped by the npm-family binding, so an unscoped twin
// wins whichever order the two arrive in.
func TestDerive_ScopedAndUnscopedTwinMergesToUnscoped(t *testing.T) {
	for name, body := range map[string]string{
		"scoped first":   "a https://h.example.com/r scoped\nb https://h.example.com/r\n",
		"unscoped first": "b https://h.example.com/r\na https://h.example.com/r scoped\n",
	} {
		t.Run(name, func(t *testing.T) {
			got := deriveScoped(t, body)
			if len(got) != 1 || len(got[0].Subtrees) != 1 {
				t.Fatalf("DeriveRows = %+v, want one host with one subtree", got)
			}
			if got[0].Subtrees[0].Scoped {
				t.Errorf("Subtrees[0] = %+v, want Scoped false", got[0].Subtrees[0])
			}
		})
	}
}

func TestDerive_ScopedTwinsStayScoped(t *testing.T) {
	got := deriveScoped(t, "a https://h.example.com/r scoped\nb https://h.example.com/r scoped\n")
	if len(got) != 1 || len(got[0].Subtrees) != 1 || !got[0].Subtrees[0].Scoped {
		t.Errorf("DeriveRows = %+v, want one scoped subtree", got)
	}
}
