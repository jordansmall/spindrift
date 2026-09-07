package credresolver

import (
	"reflect"
	"testing"
)

// TestKinds_SourceKeyOrder pins the seven-entry order that
// registryroutes.credentialSourceKeys must reproduce: the retired-key
// renderer and the "names more than one source" error text both key off
// this exact sequence.
func TestKinds_SourceKeyOrder(t *testing.T) {
	want := []string{"env", "file", "netrc", "cargo-credentials", "exec", "npmrc", "gradle-properties"}
	if got := SourceKeys(); !reflect.DeepEqual(got, want) {
		t.Errorf("SourceKeys() = %v, want %v", got, want)
	}
}

// TestKinds_CompanionKeys pins which kinds require a companion TOML key and
// what it's spelled -- registry-name and key are companions, never sources.
func TestKinds_CompanionKeys(t *testing.T) {
	want := map[string]string{
		"env":               "",
		"file":              "",
		"netrc":             "",
		"cargo-credentials": "registry-name",
		"exec":              "",
		"npmrc":             "",
		"gradle-properties": "key",
	}
	for key, companion := range want {
		k, ok := KindBySourceKey(key)
		if !ok {
			t.Fatalf("KindBySourceKey(%q): not found", key)
		}
		if k.CompanionKey != companion {
			t.Errorf("KindBySourceKey(%q).CompanionKey = %q, want %q", key, k.CompanionKey, companion)
		}
	}
}

// TestKinds_FileFormats pins New's Config.FileFormat spelling per kind --
// env and exec aren't file-backed, so theirs is "".
func TestKinds_FileFormats(t *testing.T) {
	want := map[string]string{
		"env":               "",
		"file":              "raw",
		"netrc":             "netrc",
		"cargo-credentials": "cargo-credentials",
		"exec":              "",
		"npmrc":             "npmrc",
		"gradle-properties": "gradle-properties",
	}
	for key, format := range want {
		k, ok := KindBySourceKey(key)
		if !ok {
			t.Fatalf("KindBySourceKey(%q): not found", key)
		}
		if k.FileFormat != format {
			t.Errorf("KindBySourceKey(%q).FileFormat = %q, want %q", key, k.FileFormat, format)
		}
	}
}

// TestKinds_StoreKindsOrderAndPaths pins the documented store search order
// (netrc, npmrc, cargo-credentials, gradle-properties) and each store's
// $HOME-relative path segments -- cmd/launcher/registrydiscover.go derives
// its store list by walking StoreKinds(), so this table is the single
// source of truth both must reproduce.
func TestKinds_StoreKindsOrderAndPaths(t *testing.T) {
	sk := StoreKinds()

	wantKeys := []string{"netrc", "npmrc", "cargo-credentials", "gradle-properties"}
	var gotKeys []string
	for _, k := range sk {
		gotKeys = append(gotKeys, k.SourceKey)
	}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("StoreKinds() source keys = %v, want %v", gotKeys, wantKeys)
	}

	wantPaths := map[string][]string{
		"netrc":             {".netrc"},
		"npmrc":             {".npmrc"},
		"cargo-credentials": {".cargo", "credentials.toml"},
		"gradle-properties": {".gradle", "gradle.properties"},
	}
	for _, k := range sk {
		if !reflect.DeepEqual(k.StorePath, wantPaths[k.SourceKey]) {
			t.Errorf("KindBySourceKey(%q).StorePath = %v, want %v", k.SourceKey, k.StorePath, wantPaths[k.SourceKey])
		}
	}
}

// TestKinds_StoreConfigMatchesStoreLookupConfig proves each store kind's
// StoreConfig reproduces the exact Config shape
// registrydiscover.storeLookupConfig produces for the same inputs, which
// delegates here rather than keeping a parallel copy.
func TestKinds_StoreConfigMatchesStoreLookupConfig(t *testing.T) {
	facts := StoreFacts{
		Host:            "registry.example.com",
		HostKey:         "registryExampleCom",
		UpstreamBaseURL: "https://upstream.example.com",
		RegistryName:    "myreg",
	}

	cases := []struct {
		key  string
		path string
		want Config
	}{
		{"netrc", "/home/agent/.netrc", Config{FromFile: "/home/agent/.netrc", FileFormat: "netrc", UpstreamURL: facts.UpstreamBaseURL}},
		{"npmrc", "/home/agent/.npmrc", Config{FromFile: "/home/agent/.npmrc", FileFormat: "npmrc", MatchHost: facts.Host}},
		{"cargo-credentials", "/home/agent/.cargo/credentials.toml", Config{FromFile: "/home/agent/.cargo/credentials.toml", FileFormat: "cargo-credentials", RegistryName: facts.RegistryName}},
		{"gradle-properties", "/home/agent/.gradle/gradle.properties", Config{FromFile: "/home/agent/.gradle/gradle.properties", FileFormat: "gradle-properties", PropertyKey: facts.HostKey}},
	}
	for _, tc := range cases {
		k, ok := KindBySourceKey(tc.key)
		if !ok {
			t.Fatalf("KindBySourceKey(%q): not found", tc.key)
		}
		if k.StoreConfig == nil {
			t.Fatalf("KindBySourceKey(%q).StoreConfig is nil", tc.key)
		}
		if got := k.StoreConfig(tc.path, facts); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("KindBySourceKey(%q).StoreConfig(%q, facts) = %+v, want %+v", tc.key, tc.path, got, tc.want)
		}
	}
}

// TestKinds_StoreApplicable proves only cargo-credentials' StoreApplicable
// gates on a fact (an empty RegistryName means no table to look up), and
// every other store kind's is nil or unconditionally true.
func TestKinds_StoreApplicable(t *testing.T) {
	for _, k := range StoreKinds() {
		switch k.SourceKey {
		case "cargo-credentials":
			if k.StoreApplicable == nil {
				t.Fatalf("cargo-credentials.StoreApplicable is nil, want a non-nil gate")
			}
			if k.StoreApplicable(StoreFacts{RegistryName: ""}) {
				t.Errorf("cargo-credentials.StoreApplicable(empty RegistryName) = true, want false")
			}
			if !k.StoreApplicable(StoreFacts{RegistryName: "myreg"}) {
				t.Errorf("cargo-credentials.StoreApplicable(RegistryName=myreg) = false, want true")
			}
		default:
			if k.StoreApplicable != nil && !k.StoreApplicable(StoreFacts{}) {
				t.Errorf("%s.StoreApplicable(zero StoreFacts) = false, want nil or true", k.SourceKey)
			}
		}
	}
}

// TestIsCompanionKey proves IsCompanionKey recognizes exactly the two
// companion keys and rejects every source key.
func TestIsCompanionKey(t *testing.T) {
	for _, key := range []string{"registry-name", "key"} {
		if !IsCompanionKey(key) {
			t.Errorf("IsCompanionKey(%q) = false, want true", key)
		}
	}
	for _, key := range SourceKeys() {
		if IsCompanionKey(key) {
			t.Errorf("IsCompanionKey(%q) = true, want false (source keys are never companion keys)", key)
		}
	}
}

// TestKinds_ReturnsCopy proves Kinds() hands back a copy, not the package's
// live table -- mutating the returned slice, including a nested StorePath
// element, must not affect a later call.
func TestKinds_ReturnsCopy(t *testing.T) {
	restoreStorePaths(t)

	got := Kinds()
	got[0].SourceKey = "mutated"

	mutateStorePath(t, got, "netrc")

	again := Kinds()
	if again[0].SourceKey == "mutated" {
		t.Error("Kinds() leaked its backing array: mutation of one call's result affected another call")
	}
	for _, k := range again {
		if k.SourceKey == "netrc" && k.StorePath[0] == "x" {
			t.Error("Kinds() leaked its StorePath backing array: mutating one call's StorePath affected another call")
		}
	}

	// The other two accessors hand out rows of the same table, so they owe
	// the same isolation.
	mutateStorePath(t, StoreKinds(), "npmrc")
	if sk, _ := KindBySourceKey("npmrc"); sk.StorePath[0] == "x" {
		t.Error("StoreKinds() leaked its StorePath backing array")
	}
	cargo, _ := KindBySourceKey("cargo-credentials")
	cargo.StorePath[0] = "x"
	if again, _ := KindBySourceKey("cargo-credentials"); again.StorePath[0] == "x" {
		t.Error("KindBySourceKey() leaked its StorePath backing array")
	}
}

// restoreStorePaths puts every kindTable StorePath back at test end. The
// mutations below are writes this test wants to bounce off a copy, so if
// clone() ever regresses they land in the package's own table instead --
// without this the one real failure would cascade into every later test in
// the package.
func restoreStorePaths(t *testing.T) {
	t.Helper()
	for i := range kindTable {
		if kindTable[i].StorePath == nil {
			continue
		}
		orig := append([]string(nil), kindTable[i].StorePath...)
		t.Cleanup(func() { copy(kindTable[i].StorePath, orig) })
	}
}

// mutateStorePath overwrites the first path segment of key's row in kinds,
// the write that aliases straight through to kindTable unless the accessor
// deep-copied StorePath.
func mutateStorePath(t *testing.T, kinds []Kind, key string) {
	t.Helper()
	for i, k := range kinds {
		if k.SourceKey == key {
			kinds[i].StorePath[0] = "x"
			return
		}
	}
	t.Fatalf("no %q entry to mutate", key)
}

// TestKindTable_Invariants walks kindTable directly (not Kinds()) so an
// eighth kind added with a column omitted fails here rather than
// nil-panicking later in parseCredential/Render/New.
func TestKindTable_Invariants(t *testing.T) {
	for _, k := range kindTable {
		if k.CompanionKey != "" && k.CompanionField == nil {
			t.Errorf("%s: CompanionKey = %q but CompanionField is nil", k.SourceKey, k.CompanionKey)
		}
		if !k.ArgvValue && k.ValueField == nil {
			t.Errorf("%s: ArgvValue is false but ValueField is nil", k.SourceKey)
		}
		if k.FileFormat != "" && k.newFileResolver == nil {
			t.Errorf("%s: FileFormat = %q but newFileResolver is nil", k.SourceKey, k.FileFormat)
		}
		if k.StorePath != nil && k.StoreConfig == nil {
			t.Errorf("%s: StorePath = %v but StoreConfig is nil", k.SourceKey, k.StorePath)
		}
	}

	seen := map[int]string{}
	for _, k := range kindTable {
		if k.StorePath == nil {
			continue
		}
		if prior, ok := seen[k.storeSearchOrder]; ok {
			t.Errorf("%s and %s share storeSearchOrder %d, want distinct values per store row", prior, k.SourceKey, k.storeSearchOrder)
		}
		seen[k.storeSearchOrder] = k.SourceKey
	}

	// Both columns are looked up by first match -- SourceKey through
	// KindBySourceKey, FileFormat through New -- so a duplicate row would
	// be silently shadowed rather than reported.
	sourceKeys := map[string]bool{}
	fileFormats := map[string]string{}
	for _, k := range kindTable {
		if sourceKeys[k.SourceKey] {
			t.Errorf("%s: duplicate SourceKey, want one row per key", k.SourceKey)
		}
		sourceKeys[k.SourceKey] = true

		if k.FileFormat == "" {
			continue
		}
		if prior, ok := fileFormats[k.FileFormat]; ok {
			t.Errorf("%s and %s share FileFormat %q, want one row per format", prior, k.SourceKey, k.FileFormat)
		}
		fileFormats[k.FileFormat] = k.SourceKey
	}
}
