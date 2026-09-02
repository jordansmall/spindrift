package registrydiscover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExtract_CargoSingleRegistrySparseIndex verifies that a
// [registries.NAME] table with a "sparse+https://" index URL is extracted
// into a Declared with its "sparse+" prefix stripped and CargoRegistryName
// set.
func TestExtract_CargoSingleRegistrySparseIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".cargo"), 0o755); err != nil {
		t.Fatal(err)
	}
	cargoConfig := `
[registries.mycorp]
index = "sparse+https://cargo.example.com/index/"
`
	if err := os.WriteFile(filepath.Join(dir, ".cargo", "config.toml"), []byte(cargoConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %+v, want none", notes)
	}
	if len(declared) != 1 {
		t.Fatalf("declared = %+v, want exactly 1", declared)
	}
	got := declared[0]
	want := Declared{
		Ecosystem:         "cargo",
		ConfigPath:        ".cargo/config.toml",
		Host:              "cargo.example.com",
		UpstreamBaseURL:   "https://cargo.example.com/index",
		CargoRegistryName: "mycorp",
	}
	if got != want {
		t.Errorf("declared[0] = %+v, want %+v", got, want)
	}
}

// TestExtract_CargoFilePresentNoRegistryYieldsNote verifies that a
// .cargo/config.toml present but naming no [registries.*] table yields a
// Note instead of any Declared.
func TestExtract_CargoFilePresentNoRegistryYieldsNote(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".cargo"), 0o755); err != nil {
		t.Fatal(err)
	}
	cargoConfig := `
[net]
git-fetch-with-cli = true
`
	if err := os.WriteFile(filepath.Join(dir, ".cargo", "config.toml"), []byte(cargoConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(declared) != 0 {
		t.Fatalf("declared = %+v, want none", declared)
	}
	want := []Note{{ConfigPath: ".cargo/config.toml", Ecosystem: "cargo"}}
	if len(notes) != 1 || notes[0] != want[0] {
		t.Errorf("notes = %+v, want %+v", notes, want)
	}
}

// TestExtract_CargoMalformedTOMLIsErrorNamingFile verifies that unparseable
// TOML in .cargo/config.toml surfaces as an error naming the offending
// config path, not a silently empty result.
func TestExtract_CargoMalformedTOMLIsErrorNamingFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".cargo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".cargo", "config.toml"), []byte("this is not [ valid toml"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := Extract(dir)
	if err == nil {
		t.Fatal("Extract: expected error for malformed TOML, got nil")
	}
	if !strings.Contains(err.Error(), ".cargo/config.toml") {
		t.Errorf("Extract error = %q, want it to name %q", err.Error(), ".cargo/config.toml")
	}
}

// TestExtract_NpmrcDefaultAndScopedRegistry verifies that .npmrc's unscoped
// "registry=" line and a "@scope:registry=" line both extract into their own
// Declared, comments ignored.
func TestExtract_NpmrcDefaultAndScopedRegistry(t *testing.T) {
	dir := t.TempDir()
	npmrc := `
registry=https://npm.example.com/
@myorg:registry=https://scoped.example.com/npm/
# a comment
; another comment style
`
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %+v, want none", notes)
	}
	if len(declared) != 2 {
		t.Fatalf("declared = %+v, want exactly 2", declared)
	}
	want := []Declared{
		{Ecosystem: "npm", ConfigPath: ".npmrc", Host: "npm.example.com", UpstreamBaseURL: "https://npm.example.com"},
		{Ecosystem: "npm", ConfigPath: ".npmrc", Host: "scoped.example.com", UpstreamBaseURL: "https://scoped.example.com/npm"},
	}
	for i, w := range want {
		if declared[i] != w {
			t.Errorf("declared[%d] = %+v, want %+v", i, declared[i], w)
		}
	}
}

// TestExtract_YarnrcNpmRegistryServerQuoted verifies that .yarnrc.yml's
// top-level and per-scope npmRegistryServer values extract correctly whether
// double- or single-quoted.
func TestExtract_YarnrcNpmRegistryServerQuoted(t *testing.T) {
	dir := t.TempDir()
	yarnrc := `
npmRegistryServer: "https://yarn.example.com/registry"
npmScopes:
  myorg:
    npmRegistryServer: 'https://scoped-yarn.example.com/registry/'
`
	if err := os.WriteFile(filepath.Join(dir, ".yarnrc.yml"), []byte(yarnrc), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %+v, want none", notes)
	}
	if len(declared) != 2 {
		t.Fatalf("declared = %+v, want exactly 2", declared)
	}
	want := []Declared{
		{Ecosystem: "yarn", ConfigPath: ".yarnrc.yml", Host: "yarn.example.com", UpstreamBaseURL: "https://yarn.example.com/registry"},
		{Ecosystem: "yarn", ConfigPath: ".yarnrc.yml", Host: "scoped-yarn.example.com", UpstreamBaseURL: "https://scoped-yarn.example.com/registry"},
	}
	for i, w := range want {
		if declared[i] != w {
			t.Errorf("declared[%d] = %+v, want %+v", i, declared[i], w)
		}
	}
}

// TestExtract_PnpmWorkspaceRegistryLine verifies that pnpm-workspace.yaml's
// top-level "registry:" key and a quoted "@scope:registry" catalog key both
// extract into their own Declared.
func TestExtract_PnpmWorkspaceRegistryLine(t *testing.T) {
	dir := t.TempDir()
	pnpmWorkspace := `
packages:
  - "packages/*"
registry: "https://pnpm.example.com/registry"
catalog:
  "@myorg:registry": 'https://scoped-pnpm.example.com/registry/'
`
	if err := os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"), []byte(pnpmWorkspace), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %+v, want none", notes)
	}
	if len(declared) != 2 {
		t.Fatalf("declared = %+v, want exactly 2", declared)
	}
	want := []Declared{
		{Ecosystem: "pnpm", ConfigPath: "pnpm-workspace.yaml", Host: "pnpm.example.com", UpstreamBaseURL: "https://pnpm.example.com/registry"},
		{Ecosystem: "pnpm", ConfigPath: "pnpm-workspace.yaml", Host: "scoped-pnpm.example.com", UpstreamBaseURL: "https://scoped-pnpm.example.com/registry"},
	}
	for i, w := range want {
		if declared[i] != w {
			t.Errorf("declared[%d] = %+v, want %+v", i, declared[i], w)
		}
	}
}

// TestExtract_PnpmWorkspaceNonRegistrySuffixKeyNotDeclared verifies that a
// key merely ending in "registry" (not the literal "registry" key or a
// "@scope:registry" scoped key) is not mistaken for a registry declaration.
func TestExtract_PnpmWorkspaceNonRegistrySuffixKeyNotDeclared(t *testing.T) {
	dir := t.TempDir()
	pnpmWorkspace := "myregistry: https://sneaky.example.com\n"
	if err := os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"), []byte(pnpmWorkspace), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(declared) != 0 {
		t.Fatalf("declared = %+v, want none (\"myregistry\" is not a real pnpm registry key)", declared)
	}
	want := []Note{{ConfigPath: "pnpm-workspace.yaml", Ecosystem: "pnpm", Skipped: false}}
	if len(notes) != 1 || notes[0] != want[0] {
		t.Errorf("notes = %+v, want %+v", notes, want)
	}
}

// TestExtract_PnpmWorkspaceListItemRegistryKeyNotDeclared verifies that a
// "registry:" key nested under a YAML list item (not a top-level or scoped
// key) is not extracted as a declaration.
func TestExtract_PnpmWorkspaceListItemRegistryKeyNotDeclared(t *testing.T) {
	dir := t.TempDir()
	pnpmWorkspace := `
mirrors:
  - registry: https://listitem.example.com
`
	if err := os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"), []byte(pnpmWorkspace), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(declared) != 0 {
		t.Fatalf("declared = %+v, want none (a YAML list item is not a top-level or scoped registry key)", declared)
	}
	want := []Note{{ConfigPath: "pnpm-workspace.yaml", Ecosystem: "pnpm", Skipped: false}}
	if len(notes) != 1 || notes[0] != want[0] {
		t.Errorf("notes = %+v, want %+v", notes, want)
	}
}

// TestExtract_NoConfigFilesYieldsEmptyResults verifies that a directory with
// none of the recognized config files yields empty Declared and Note slices,
// not an error.
func TestExtract_NoConfigFilesYieldsEmptyResults(t *testing.T) {
	dir := t.TempDir()

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(declared) != 0 {
		t.Errorf("declared = %+v, want none", declared)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %+v, want none", notes)
	}
}

// TestExtract_NpmrcUserinfoURLIsSkippedNotStored verifies that a "registry="
// URL embedding a credential in its userinfo component is never stored as a
// Declared, since that would persist the credential into a written routes
// file.
func TestExtract_NpmrcUserinfoURLIsSkippedNotStored(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=https://ci:s3cr3t@npm.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(declared) != 0 {
		t.Fatalf("declared = %+v, want none (userinfo URL must never be stored)", declared)
	}
	want := []Note{{ConfigPath: ".npmrc", Ecosystem: "npm", Skipped: true}}
	if len(notes) != 1 || notes[0] != want[0] {
		t.Errorf("notes = %+v, want %+v", notes, want)
	}
}

// TestExtract_NpmrcPortOnlyHostIsSkippedNotStored verifies that a
// "registry=" URL with a port but no hostname (e.g. "http://:8080/") is
// skipped rather than stored, since it has no host a route can match on.
func TestExtract_NpmrcPortOnlyHostIsSkippedNotStored(t *testing.T) {
	dir := t.TempDir()
	npmrc := "registry=http://:8080/\n"
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(declared) != 0 {
		t.Fatalf("declared = %+v, want none (a port-only host has no hostname a route can match on)", declared)
	}
	want := []Note{{ConfigPath: ".npmrc", Ecosystem: "npm", Skipped: true}}
	if len(notes) != 1 || notes[0] != want[0] {
		t.Errorf("notes = %+v, want %+v", notes, want)
	}
}

// TestExtract_CargoFileSchemeIndexIsSkippedWithDistinctNote verifies that a
// cargo registry index using a "file://" URL is skipped and noted distinctly
// from "no registry declared at all" (Note.Skipped=true).
func TestExtract_CargoFileSchemeIndexIsSkippedWithDistinctNote(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".cargo"), 0o755); err != nil {
		t.Fatal(err)
	}
	cargoConfig := `
[registries.mirror]
index = "file:///srv/mirror"
`
	if err := os.WriteFile(filepath.Join(dir, ".cargo", "config.toml"), []byte(cargoConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(declared) != 0 {
		t.Fatalf("declared = %+v, want none", declared)
	}
	want := []Note{{ConfigPath: ".cargo/config.toml", Ecosystem: "cargo", Skipped: true}}
	if len(notes) != 1 || notes[0] != want[0] {
		t.Errorf("notes = %+v, want %+v (a non-http index must not read as \"no registry declared\")", notes, want)
	}
}

// TestExtract_YarnrcNonHTTPRegistryIsSkippedWithDistinctNote verifies that a
// .yarnrc.yml npmRegistryServer value that is a local path, not an http(s)
// URL, is skipped and noted distinctly (Note.Skipped=true).
func TestExtract_YarnrcNonHTTPRegistryIsSkippedWithDistinctNote(t *testing.T) {
	dir := t.TempDir()
	yarnrc := "npmRegistryServer: /local/path\n"
	if err := os.WriteFile(filepath.Join(dir, ".yarnrc.yml"), []byte(yarnrc), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(declared) != 0 {
		t.Fatalf("declared = %+v, want none", declared)
	}
	want := []Note{{ConfigPath: ".yarnrc.yml", Ecosystem: "yarn", Skipped: true}}
	if len(notes) != 1 || notes[0] != want[0] {
		t.Errorf("notes = %+v, want %+v", notes, want)
	}
}

// TestExtract_YarnrcFullLineCommentYieldsNoDeclaration verifies that a
// .yarnrc.yml line that is entirely a "#" comment is never mistaken for a
// registry declaration.
func TestExtract_YarnrcFullLineCommentYieldsNoDeclaration(t *testing.T) {
	dir := t.TempDir()
	yarnrc := "# npmRegistryServer: https://commented-out.example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, ".yarnrc.yml"), []byte(yarnrc), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(declared) != 0 {
		t.Fatalf("declared = %+v, want none (a commented-out registry line must not leak a Declared row)", declared)
	}
	want := []Note{{ConfigPath: ".yarnrc.yml", Ecosystem: "yarn", Skipped: false}}
	if len(notes) != 1 || notes[0] != want[0] {
		t.Errorf("notes = %+v, want %+v", notes, want)
	}
}

// TestExtract_PnpmWorkspaceFullLineCommentYieldsNoDeclaration verifies that a
// pnpm-workspace.yaml line that is entirely a "#" comment is never mistaken
// for a registry declaration.
func TestExtract_PnpmWorkspaceFullLineCommentYieldsNoDeclaration(t *testing.T) {
	dir := t.TempDir()
	pnpmWorkspace := `
packages:
  - "packages/*"
# registry: https://commented-out.example.com/
`
	if err := os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"), []byte(pnpmWorkspace), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(declared) != 0 {
		t.Fatalf("declared = %+v, want none (a commented-out registry line must not leak a Declared row)", declared)
	}
	want := []Note{{ConfigPath: "pnpm-workspace.yaml", Ecosystem: "pnpm", Skipped: false}}
	if len(notes) != 1 || notes[0] != want[0] {
		t.Errorf("notes = %+v, want %+v", notes, want)
	}
}

// TestExtract_YarnrcTrailingInlineCommentStripped verifies that a
// space-then-"#" trailing comment after a .yarnrc.yml registry value is
// stripped from the extracted URL.
func TestExtract_YarnrcTrailingInlineCommentStripped(t *testing.T) {
	dir := t.TempDir()
	yarnrc := "npmRegistryServer: https://yarn.example.com # our mirror\n"
	if err := os.WriteFile(filepath.Join(dir, ".yarnrc.yml"), []byte(yarnrc), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %+v, want none", notes)
	}
	if len(declared) != 1 {
		t.Fatalf("declared = %+v, want exactly 1", declared)
	}
	want := Declared{Ecosystem: "yarn", ConfigPath: ".yarnrc.yml", Host: "yarn.example.com", UpstreamBaseURL: "https://yarn.example.com"}
	if declared[0] != want {
		t.Errorf("declared[0] = %+v, want %+v", declared[0], want)
	}
}

// TestExtract_YarnrcTabBeforeTrailingInlineCommentStripped is the tab-
// separator variant of TestExtract_YarnrcTrailingInlineCommentStripped.
func TestExtract_YarnrcTabBeforeTrailingInlineCommentStripped(t *testing.T) {
	dir := t.TempDir()
	yarnrc := "npmRegistryServer: https://yarn.example.com\t# our mirror\n"
	if err := os.WriteFile(filepath.Join(dir, ".yarnrc.yml"), []byte(yarnrc), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %+v, want none", notes)
	}
	if len(declared) != 1 {
		t.Fatalf("declared = %+v, want exactly 1", declared)
	}
	want := Declared{Ecosystem: "yarn", ConfigPath: ".yarnrc.yml", Host: "yarn.example.com", UpstreamBaseURL: "https://yarn.example.com"}
	if declared[0] != want {
		t.Errorf("declared[0] = %+v, want %+v", declared[0], want)
	}
}

// TestExtract_PnpmWorkspaceTrailingInlineCommentStripped verifies that a
// space-then-"#" trailing comment after a pnpm-workspace.yaml registry value
// is stripped from the extracted URL.
func TestExtract_PnpmWorkspaceTrailingInlineCommentStripped(t *testing.T) {
	dir := t.TempDir()
	pnpmWorkspace := "registry: https://pnpm.example.com/registry # our mirror\n"
	if err := os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"), []byte(pnpmWorkspace), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %+v, want none", notes)
	}
	if len(declared) != 1 {
		t.Fatalf("declared = %+v, want exactly 1", declared)
	}
	want := Declared{Ecosystem: "pnpm", ConfigPath: "pnpm-workspace.yaml", Host: "pnpm.example.com", UpstreamBaseURL: "https://pnpm.example.com/registry"}
	if declared[0] != want {
		t.Errorf("declared[0] = %+v, want %+v", declared[0], want)
	}
}

// TestExtract_YarnrcQuotedValueHashFragmentNotTreatedAsComment verifies that
// a "#" inside a quoted .yarnrc.yml value (a URL fragment) is preserved, not
// mistaken for a comment marker.
func TestExtract_YarnrcQuotedValueHashFragmentNotTreatedAsComment(t *testing.T) {
	dir := t.TempDir()
	yarnrc := "npmRegistryServer: \"https://yarn.example.com/#frag\"\n"
	if err := os.WriteFile(filepath.Join(dir, ".yarnrc.yml"), []byte(yarnrc), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %+v, want none", notes)
	}
	if len(declared) != 1 {
		t.Fatalf("declared = %+v, want exactly 1", declared)
	}
	want := Declared{Ecosystem: "yarn", ConfigPath: ".yarnrc.yml", Host: "yarn.example.com", UpstreamBaseURL: "https://yarn.example.com/#frag"}
	if declared[0] != want {
		t.Errorf("declared[0] = %+v, want %+v (a quoted \"#\" must not be mistaken for a trailing comment)", declared[0], want)
	}
}

// TestExtract_YarnrcUnquotedHashFragmentPlusTrailingCommentStripped covers a
// value with a "#" URL fragment (no preceding whitespace, so not a comment
// itself) followed by a whitespace-then-"#" trailing comment: the fragment's
// "#" is the first "#" in the value, so a scan that stops at the first "#"
// mistakes the fragment for the comment start and leaves the real trailing
// comment attached to the extracted URL.
func TestExtract_YarnrcUnquotedHashFragmentPlusTrailingCommentStripped(t *testing.T) {
	dir := t.TempDir()
	yarnrc := "npmRegistryServer: https://yarn.example.com/#frag # our mirror\n"
	if err := os.WriteFile(filepath.Join(dir, ".yarnrc.yml"), []byte(yarnrc), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %+v, want none", notes)
	}
	if len(declared) != 1 {
		t.Fatalf("declared = %+v, want exactly 1", declared)
	}
	want := Declared{Ecosystem: "yarn", ConfigPath: ".yarnrc.yml", Host: "yarn.example.com", UpstreamBaseURL: "https://yarn.example.com/#frag"}
	if declared[0] != want {
		t.Errorf("declared[0] = %+v, want %+v (a trailing comment after a \"#\" fragment must still be stripped)", declared[0], want)
	}
}

// TestExtract_YarnrcUnquotedHashFragmentPlusTabTrailingCommentStripped is the
// tab-separator variant of
// TestExtract_YarnrcUnquotedHashFragmentPlusTrailingCommentStripped.
func TestExtract_YarnrcUnquotedHashFragmentPlusTabTrailingCommentStripped(t *testing.T) {
	dir := t.TempDir()
	yarnrc := "npmRegistryServer: https://yarn.example.com/#frag\t# our mirror\n"
	if err := os.WriteFile(filepath.Join(dir, ".yarnrc.yml"), []byte(yarnrc), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, notes, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %+v, want none", notes)
	}
	if len(declared) != 1 {
		t.Fatalf("declared = %+v, want exactly 1", declared)
	}
	want := Declared{Ecosystem: "yarn", ConfigPath: ".yarnrc.yml", Host: "yarn.example.com", UpstreamBaseURL: "https://yarn.example.com/#frag"}
	if declared[0] != want {
		t.Errorf("declared[0] = %+v, want %+v (a trailing comment after a \"#\" fragment must still be stripped)", declared[0], want)
	}
}

// TestExtract_CargoTwoRegistriesSortedByName verifies that multiple
// [registries.NAME] tables in .cargo/config.toml come back sorted by
// registry name, independent of their order in the file.
func TestExtract_CargoTwoRegistriesSortedByName(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".cargo"), 0o755); err != nil {
		t.Fatal(err)
	}
	cargoConfig := `
[registries.zeta]
index = "sparse+https://zeta.example.com/index/"

[registries.alpha]
index = "sparse+https://alpha.example.com/index/"
`
	if err := os.WriteFile(filepath.Join(dir, ".cargo", "config.toml"), []byte(cargoConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	declared, _, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if len(declared) != 2 {
		t.Fatalf("declared = %+v, want exactly 2", declared)
	}
	if declared[0].CargoRegistryName != "alpha" || declared[1].CargoRegistryName != "zeta" {
		t.Errorf("declared order = [%s, %s], want [alpha, zeta]", declared[0].CargoRegistryName, declared[1].CargoRegistryName)
	}
}
