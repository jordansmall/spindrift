package registrypathset

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registryvocab"
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

// Discover dedupes by host and drops the second registry. Derive must not: a
// repo declaring an internal and a remote cargo registry on one host needs both
// index subtrees, or the enforced path set refuses the crates the second
// registry serves.
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
		Subtrees: []registryvocab.Subtree{
			{Ecosystem: "cargo", Path: "/artifactory/api/cargo/internal/index", RegistryName: "internal"},
			{Ecosystem: "cargo", Path: "/artifactory/api/cargo/remote/index", RegistryName: "remote"},
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Derive = %+v, want %+v", got, want)
	}
}

// Two declarations disagreeing on scheme and port still merge into one
// HostPathSet, because enforcement keys on the registryvocab.HostKey-normalized
// host and a second entry for that host would be unreachable. ecosystem.Table
// runs npm before yarn, so the .npmrc declaration comes first and its https
// Origin is the one kept.
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
		Subtrees: []registryvocab.Subtree{
			{Ecosystem: "npm", Path: "/npm"},
			{Ecosystem: "yarn", Path: "/yarn"},
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Derive = %+v, want %+v", got, want)
	}
}

// The two hosts differ only in case deliberately: equivalent hosts but distinct
// strings, so the npm row's byte-equal dedupe passes both through and Derive's
// own dedupe, over the registryvocab.HostKey-folded host, is what must collapse
// them.
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
		Subtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Derive = %+v, want %+v", got, want)
	}
}

// Only a port-less IPv6 literal reaches registryvocab.HostKey's bracket branch.
// Host unbrackets to stay comparable with a route's match-host, while Origin
// keeps the brackets because that is the authority a client dials.
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
		Subtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Derive = %+v, want %+v", got, want)
	}
}

// Absence of declaration means absence of binding: neither an empty repo nor
// one declaring only cargo may derive an npm path, because nothing in the
// snapshot names an npm registry.
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
			Subtrees: []registryvocab.Subtree{{Ecosystem: "cargo", Path: "/index", RegistryName: "mycorp"}},
		}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Derive = %+v, want %+v", got, want)
		}
	})
}

// A public registry declared with no path at all (the shape of a stock .npmrc)
// is a whole-host registry, so it derives the root subtree rather than nothing.
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
		Subtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Derive = %+v, want %+v", got, want)
	}
}

// Host is registryvocab.HostKey-normalized so it compares equal to a route's
// match-host, while Origin keeps the port (and the case url.Parse preserves) so
// it stays a usable upstream origin.
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
		Subtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/repo/npm"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Derive = %+v, want %+v", got, want)
	}
}

// This test is acceptance criterion 3, over the shape the 2026-09-04 field run
// hit: an internal and a remote cargo registry on one Artifactory host, with
// crates.io replaced through the remote. The derived set must admit that run's
// real request paths and refuse the host's other registry API endpoints.
func TestDerive_ArtifactoryFieldShape(t *testing.T) {
	dir := t.TempDir()
	// The [source.*] stanzas send crates.io traffic to the remote index. The
	// cargo row's ConfigParser reads only [registries.*], which is enough here
	// because the remote registry the replacement points at is itself declared.
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
		"/artifactory/api/cargo/remote/index/../../security/token",
		// The cargo download endpoint is a sibling of the index, not under it,
		// so nothing committed in the repo declares it and refusing it is
		// correct today. Spec #3253 decision 3 has the Forwarder learn the dl
		// base from upstream's config.json in a later ticket. This derivation
		// must not invent a dl path.
		"/artifactory/api/cargo/remote/v1/crates/axum/0.7.5/download",
	}
	for _, p := range refuse {
		if set.admits(p) {
			t.Errorf("Admits(%q) = true, want false: not a declared subtree", p)
		}
	}
}

// inTreeFixtures maps an ecosystem.Table row name to a minimal config body for
// that row's InTreeConfigPath, each declaring one distinguishable path.
var inTreeFixtures = map[string]string{
	"cargo": "[registries.mycorp]\nindex = \"sparse+https://cargo.example.com/repo/cargo/index\"\n",
	"npm":   "registry=https://npm.example.com/repo/npm\n",
	"yarn":  "npmRegistryServer: https://yarn.example.com/repo/yarn\n",
	"pnpm":  "registry: https://pnpm.example.com/repo/pnpm\n",
}

// ecosystem.TestConfigParserMatchesInTreeConfigPath proves an in-tree row has a
// parser; this one proves the row's declaration reaches a subtree. An ecosystem
// whose config parses but derives no path would leave the proxy refusing every
// request that ecosystem makes, with no other test failing. Rows with an empty
// InTreeConfigPath (go, gradle) derive nothing by design and are excluded.
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
					if !hps.admits(s.Path + "/config.json") {
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

// The snapshot directory alone is the input, so two calls over one unchanged
// fixture must agree exactly. Map iteration order in the grouping would break
// this.
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
