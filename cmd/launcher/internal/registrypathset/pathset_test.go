package registrypathset

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
)

// writeFixture writes body to repo-relative path rel under dir, creating any
// parent directories -- the same t.TempDir() fixture-repo convention
// registrydiscover's own tests use, so a path-set test reads as the repo tree
// an operator would actually commit.
func writeFixture(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDerive_TwoCargoRegistriesOneHost is the derivation's central departure
// from Discover, which dedupes by host and drops the second registry: an
// Artifactory-shaped repo declaring an internal and a remote cargo registry on
// one host must derive both index subtrees, or the enforced path-set would
// refuse the crates the second registry serves.
func TestDerive_TwoCargoRegistriesOneHost(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, ".cargo/config.toml", `
[registries.internal]
index = "sparse+https://artifacts.example.com/artifactory/api/cargo/internal/index"

[registries.remote]
index = "sparse+https://artifacts.example.com/artifactory/api/cargo/remote/index"
`)

	got, err := Derive(dir)
	if err != nil {
		t.Fatalf("Derive: unexpected error: %v", err)
	}
	want := []HostPathSet{{
		Host:   "artifacts.example.com",
		Origin: "https://artifacts.example.com",
		Subtrees: []Subtree{
			{Ecosystem: "cargo", Path: "/artifactory/api/cargo/internal/index", CargoRegistryName: "internal"},
			{Ecosystem: "cargo", Path: "/artifactory/api/cargo/remote/index", CargoRegistryName: "remote"},
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Derive = %+v, want %+v", got, want)
	}
}

// TestDerive_SameHostOriginDisagreementMerges pins the same-host merge across
// ecosystems and files: two declarations disagreeing on scheme and port still
// yield one HostPathSet holding both subtrees, since enforcement keys on the
// registryvocab.HostKey-normalized host a route matched and a second entry
// for that host would be unreachable. ecosystem.Table runs npm before yarn,
// so the .npmrc declaration is the first one and its https Origin is the
// one kept.
func TestDerive_SameHostOriginDisagreementMerges(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, ".npmrc", "registry=https://host.example.com/npm\n")
	writeFixture(t, dir, ".yarnrc.yml", "npmRegistryServer: http://host.example.com:8080/yarn\n")

	got, err := Derive(dir)
	if err != nil {
		t.Fatalf("Derive: unexpected error: %v", err)
	}
	want := []HostPathSet{{
		Host:   "host.example.com",
		Origin: "https://host.example.com",
		Subtrees: []Subtree{
			{Ecosystem: "npm", Path: "/npm"},
			{Ecosystem: "yarn", Path: "/yarn"},
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Derive = %+v, want %+v", got, want)
	}
}

// TestDerive_ExactRepeatWithinHostDedupes pins the exact-repeat drop: two
// declarations of one subtree contribute one. The two hosts differ only in
// case deliberately -- equivalent hosts but distinct strings, so extractNpm's
// own byte-equal dedupe passes both through and Derive's own dedupe, over the
// registryvocab.HostKey-folded host, is what has to collapse them.
func TestDerive_ExactRepeatWithinHostDedupes(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, ".npmrc", "registry=https://x.example.com/npm\n@scope:registry=https://X.example.com/npm\n")

	got, err := Derive(dir)
	if err != nil {
		t.Fatalf("Derive: unexpected error: %v", err)
	}
	want := []HostPathSet{{
		Host:     "x.example.com",
		Origin:   "https://x.example.com",
		Subtrees: []Subtree{{Ecosystem: "npm", Path: "/npm"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Derive = %+v, want %+v", got, want)
	}
}

// TestDerive_IPv6HostUnbracketsButOriginKeepsBrackets pins
// registryvocab.HostKey's bracket branch, which only a port-less IPv6
// literal reaches: Host must unbracket, to stay comparable with a route's
// own match-host, while Origin keeps the brackets, since that is the
// authority a client actually dials.
func TestDerive_IPv6HostUnbracketsButOriginKeepsBrackets(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, ".npmrc", "registry=http://[::1]/npm\n")

	got, err := Derive(dir)
	if err != nil {
		t.Fatalf("Derive: unexpected error: %v", err)
	}
	want := []HostPathSet{{
		Host:     "::1",
		Origin:   "http://[::1]",
		Subtrees: []Subtree{{Ecosystem: "npm", Path: "/npm"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Derive = %+v, want %+v", got, want)
	}
}

// TestDerive_NoNpmConfigDerivesNoNpmPaths pins absence of declaration to
// absence of binding: neither an empty repo nor one declaring only cargo may
// derive an npm path, since nothing in the snapshot names an npm registry.
func TestDerive_NoNpmConfigDerivesNoNpmPaths(t *testing.T) {
	t.Run("empty repo", func(t *testing.T) {
		got, err := Derive(t.TempDir())
		if err != nil {
			t.Fatalf("Derive: unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("Derive = %+v, want no host path sets", got)
		}
	})

	t.Run("cargo but no npm", func(t *testing.T) {
		dir := t.TempDir()
		writeFixture(t, dir, ".cargo/config.toml", `
[registries.mycorp]
index = "sparse+https://cargo.example.com/index"
`)

		got, err := Derive(dir)
		if err != nil {
			t.Fatalf("Derive: unexpected error: %v", err)
		}
		want := []HostPathSet{{
			Host:     "cargo.example.com",
			Origin:   "https://cargo.example.com",
			Subtrees: []Subtree{{Ecosystem: "cargo", Path: "/index", CargoRegistryName: "mycorp"}},
		}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Derive = %+v, want %+v", got, want)
		}
	})
}

// TestDerive_BareHostDerivesRootSubtree pins the bare-host rule: a public
// registry declared with no path at all (the shape of a stock .npmrc) is a
// whole-host registry, so it derives the root subtree rather than nothing.
func TestDerive_BareHostDerivesRootSubtree(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, ".npmrc", "registry=https://registry.npmjs.org\n")

	got, err := Derive(dir)
	if err != nil {
		t.Fatalf("Derive: unexpected error: %v", err)
	}
	want := []HostPathSet{{
		Host:     "registry.npmjs.org",
		Origin:   "https://registry.npmjs.org",
		Subtrees: []Subtree{{Ecosystem: "npm", Path: "/"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Derive = %+v, want %+v", got, want)
	}
}

// TestDerive_HostNormalizedOriginKeepsPort pins the two host renderings apart:
// Host is registryvocab.HostKey-normalized so it compares equal to a
// route's match-host, while Origin keeps the port (and the case url.Parse
// preserves) so it stays a usable upstream origin.
func TestDerive_HostNormalizedOriginKeepsPort(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, ".npmrc", "registry=https://HOST.example.com:8443/repo/npm\n")

	got, err := Derive(dir)
	if err != nil {
		t.Fatalf("Derive: unexpected error: %v", err)
	}
	want := []HostPathSet{{
		Host:     "host.example.com",
		Origin:   "https://HOST.example.com:8443",
		Subtrees: []Subtree{{Ecosystem: "npm", Path: "/repo/npm"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Derive = %+v, want %+v", got, want)
	}
}

// TestHostPathSet_Admits pins the membership rule the enforcement point will
// ask of a derived set: a subtree root covers itself and everything below it
// at a segment boundary, and nothing else. The sibling-prefix and traversal
// cases are the ones a bare strings.HasPrefix would wrongly admit.
func TestHostPathSet_Admits(t *testing.T) {
	set := HostPathSet{
		Host:   "artifacts.example.com",
		Origin: "https://artifacts.example.com",
		Subtrees: []Subtree{
			{Ecosystem: "cargo", Path: "/artifactory/api/cargo/remote/index", CargoRegistryName: "remote"},
			{Ecosystem: "npm", Path: "/artifactory/api/npm/remote"},
		},
	}

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"subtree root itself", "/artifactory/api/cargo/remote/index", true},
		{"file directly under the root", "/artifactory/api/cargo/remote/index/config.json", true},
		{"deep path under the root", "/artifactory/api/cargo/remote/index/ax/um/axum", true},
		{"trailing slash cleans to the root", "/artifactory/api/cargo/remote/index/", true},
		{"dot segment cleans away", "/artifactory/api/cargo/remote/index/./config.json", true},
		{"second subtree", "/artifactory/api/npm/remote/axios", true},
		{"sibling sharing a prefix but not a segment boundary", "/artifactory/api/cargo/remote/indexfoo", false},
		{"parent of the root", "/artifactory/api/cargo/remote", false},
		{"registry API surface", "/artifactory/api/security/token", false},
		{"traversal escaping the subtree", "/artifactory/api/cargo/remote/index/../../security/token", false},
		{"relative path is not an absolute request path", "artifactory/api/cargo/remote/index", false},
		{"empty path", "", false},
		{"traversal above root", "/../etc/passwd", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := set.Admits(tc.path); got != tc.want {
				t.Errorf("Admits(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// TestHostPathSet_Admits_RootSubtree pins the bare-host declaration's meaning
// on the membership side: Derive renders it as the root subtree, so the whole
// host is the registry and every path on it is admitted.
func TestHostPathSet_Admits_RootSubtree(t *testing.T) {
	set := HostPathSet{Host: "registry.npmjs.org", Subtrees: []Subtree{{Ecosystem: "npm", Path: "/"}}}
	for _, p := range []string{"/", "/axios", "/@myorg/pkg/-/pkg-1.0.0.tgz"} {
		if !set.Admits(p) {
			t.Errorf("Admits(%q) = false, want true under the root subtree", p)
		}
	}
	if set.Admits("axios") {
		t.Error("Admits(\"axios\") = true, want false: not an absolute request path")
	}
}

// TestHostPathSet_Admits_NoSubtreesAdmitsNothing pins the fail-closed end of
// "absence of declaration is absence of binding": a host entry that derived no
// subtree admits no path at all, rather than degenerating into allow-everything.
func TestHostPathSet_Admits_NoSubtreesAdmitsNothing(t *testing.T) {
	var set HostPathSet
	for _, p := range []string{"/", "/index", "/artifactory/api/security/token"} {
		if set.Admits(p) {
			t.Errorf("Admits(%q) = true, want false for a set with no subtrees", p)
		}
	}
}

// TestDerive_ArtifactoryFieldShape is acceptance criterion 3: the shape the
// 2026-09-04 field run actually hit -- an internal and a remote cargo registry
// on one Artifactory host, with crates.io replaced through the remote so
// public dependencies resolve there -- must derive a set that admits that
// run's real request paths and refuses the host's registry API surface.
func TestDerive_ArtifactoryFieldShape(t *testing.T) {
	dir := t.TempDir()
	// [source.crates-io]/[source.remote] is the stanza that makes crates.io
	// traffic land on the remote index. registrydiscover's cargo extractor
	// reads only [registries.*], which is sufficient here: the remote
	// registry the replacement points at is itself declared, so its subtree
	// is derived either way.
	writeFixture(t, dir, ".cargo/config.toml", `
[registries.internal]
index = "sparse+https://artifacts.example.com/artifactory/api/cargo/internal/index"

[registries.remote]
index = "sparse+https://artifacts.example.com/artifactory/api/cargo/remote/index"

[source.crates-io]
replace-with = "remote"

[source.remote]
registry = "sparse+https://artifacts.example.com/artifactory/api/cargo/remote/index"
`)

	got, err := Derive(dir)
	if err != nil {
		t.Fatalf("Derive: unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Derive = %+v, want exactly one host path set", got)
	}
	set := got[0]
	if set.Host != "artifacts.example.com" {
		t.Fatalf("Host = %q, want %q", set.Host, "artifacts.example.com")
	}

	admit := []string{
		"/artifactory/api/cargo/internal/index",
		"/artifactory/api/cargo/internal/index/config.json",
		"/artifactory/api/cargo/internal/index/ax/um/axum",
		"/artifactory/api/cargo/remote/index",
		"/artifactory/api/cargo/remote/index/config.json",
		"/artifactory/api/cargo/remote/index/ax/um/axum",
	}
	for _, p := range admit {
		if !set.Admits(p) {
			t.Errorf("Admits(%q) = false, want true: a real field-run request path", p)
		}
	}

	refuse := []string{
		// Credential-minting and administrative endpoints on the same host.
		// No declaration names them, so the operator credential must never be
		// steerable at them.
		"/artifactory/api/security/token",
		"/artifactory/api/repositories",
		"/artifactory/api/cargo/remote/index/../../security/token",
		// The cargo download endpoint is a sibling of the index, not under
		// it, so nothing committed in the repo declares it and refusing it is
		// correct today. Spec #3253 decision 3 has the Forwarder learn the dl
		// base from upstream's own config.json and add that subtree,
		// same-host pinned -- a later ticket. This derivation must not invent
		// a dl path here.
		"/artifactory/api/cargo/remote/v1/crates/axum/0.7.5/download",
	}
	for _, p := range refuse {
		if set.Admits(p) {
			t.Errorf("Admits(%q) = true, want false: not a declared subtree", p)
		}
	}
}

// inTreeFixtures maps an ecosystem.Table row name to a minimal committed
// config body for that row's InTreeConfigPath, declaring one registry with a
// distinguishable path.
var inTreeFixtures = map[string]string{
	"cargo": "[registries.mycorp]\nindex = \"sparse+https://cargo.example.com/repo/cargo/index\"\n",
	"npm":   "registry=https://npm.example.com/repo/npm\n",
	"yarn":  "npmRegistryServer: https://yarn.example.com/repo/yarn\n",
	"pnpm":  "registry: https://pnpm.example.com/repo/pnpm\n",
}

// TestDerive_CoversEveryInTreeEcosystem guards the derivation the way
// TestExtractors_MatchInTreeRows guards the extractors, one step further
// along: that guard proves an in-tree ecosystem.Table row has a parser, while
// this one proves the row's parsed declaration actually reaches a subtree. An
// ecosystem whose config parses but silently derives no path would leave the
// proxy refusing every request that ecosystem makes, with no other test
// failing. Rows with an empty InTreeConfigPath derive nothing by design -- go,
// whose path lives on the route rather than in the repo, and gradle, which
// commits no config this scan can read -- so they are excluded here rather
// than left unstated.
func TestDerive_CoversEveryInTreeEcosystem(t *testing.T) {
	inTree := make(map[string]string)
	for _, row := range ecosystem.Table {
		if row.InTreeConfigPath != "" {
			inTree[row.Name] = row.InTreeConfigPath
		}
	}

	for name := range inTree {
		if _, ok := inTreeFixtures[name]; !ok {
			t.Errorf("ecosystem.Table row %q has a non-empty InTreeConfigPath but no entry in inTreeFixtures", name)
		}
	}
	for name := range inTreeFixtures {
		if _, ok := inTree[name]; !ok {
			t.Errorf("inTreeFixtures has an entry for %q, but no ecosystem.Table row of that name has a non-empty InTreeConfigPath", name)
		}
	}

	for name, configPath := range inTree {
		body, ok := inTreeFixtures[name]
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, configPath, body)

			got, err := Derive(dir)
			if err != nil {
				t.Fatalf("Derive: unexpected error: %v", err)
			}
			found := false
			for _, hps := range got {
				for _, s := range hps.Subtrees {
					if s.Ecosystem != name {
						continue
					}
					found = true
					if !hps.Admits(s.Path + "/config.json") {
						t.Errorf("derived %s subtree %q does not admit a path under itself", name, s.Path)
					}
				}
			}
			if !found {
				t.Errorf("Derive over a %s fixture at %s = %+v, want a %s subtree", name, configPath, got, name)
			}
		})
	}
}

// TestDerive_DeterministicOverSnapshot pins the acceptance criterion that the
// snapshot directory alone is the input: two calls over one unchanged fixture
// must agree exactly, which map iteration order in the grouping would break.
func TestDerive_DeterministicOverSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, ".cargo/config.toml", `
[registries.zeta]
index = "sparse+https://artifacts.example.com/artifactory/api/cargo/zeta/index"

[registries.alpha]
index = "sparse+https://artifacts.example.com/artifactory/api/cargo/alpha/index"
`)
	writeFixture(t, dir, ".npmrc", "registry=https://npm.example.com/repo\n@myorg:registry=https://other.example.com/repo\n")
	writeFixture(t, dir, ".yarnrc.yml", "npmRegistryServer: https://yarn.example.com/repo\n")

	first, err := Derive(dir)
	if err != nil {
		t.Fatalf("Derive: unexpected error: %v", err)
	}
	second, err := Derive(dir)
	if err != nil {
		t.Fatalf("Derive: unexpected error: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("Derive twice over one snapshot disagreed:\nfirst  = %+v\nsecond = %+v", first, second)
	}
}
