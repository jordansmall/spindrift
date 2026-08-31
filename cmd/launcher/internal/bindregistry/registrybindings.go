package bindregistry

import "fmt"

// NpmFamilyBindings mirrors the npm/pnpm/yarn berry portion of the deleted
// entrypoint.sh phase_registry_proxy_forwarder (see git history): all three
// package managers get pointed at the same Forwarder URL, each via the one
// env-var-override mechanism that beats a Target repo's own committed
// project-level config (npm's env > project .npmrc > user .npmrc > global
// .npmrc; pnpm's own pnpm_config_* prefix, since it no longer honors
// npm_config_*; yarn berry's YARN_<KEY> single-key override convention).
// Unlike cargo, npm has no per-registry table -- the env var overrides its
// one default registry outright, and it wins even over a Target repo's own
// committed project-level .npmrc. These bindings cover packument/metadata
// requests only, not the tarball fetch that follows: npm's packument JSON
// embeds an absolute tarball URL that pacote fetches verbatim rather than
// deriving it from this registry setting, so that request leaves the proxy
// and reaches upstream directly, unauthenticated -- the same accepted gap
// ADR 0044 documents for cargo's own download endpoint (see ADR 0044's
// Update, issue #2854); a documented, accepted gap, not an oversight.
// Unscoped only -- per-scope registry entries stay entrypoint-side, applied
// by the *_intree_binding_apply phases.
func NpmFamilyBindings(port int) []EnvExport {
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	return []EnvExport{
		{Name: "npm_config_registry", Value: url},
		{Name: "pnpm_config_registry", Value: url},
		{Name: "YARN_NPM_REGISTRY_SERVER", Value: url},
	}
}

// CargoConfigTOML renders the $CARGO_HOME/config.toml content, mirroring the
// heredoc from the deleted entrypoint.sh phase_registry_proxy_forwarder (see
// git history) verbatim. Cargo's crates-io source-replacement config is
// table-valued, and Cargo does not proxy table-valued config through its
// CARGO_<SECTION>_<KEY> env-var mechanism (cargo#5416, still open) -- so
// unlike Go or npm this binding can only be applied by writing a file, not
// by exporting an env var. driver-exec bind-registry's bindings mode
// (runBindRegistryBindings in cmd/launcher/driver-exec/bindregistry_cmd.go)
// resolves $CARGO_HOME and writes this content to disk; this function stays
// a pure string-builder so it's unit-testable without touching a
// filesystem. Cargo's sparse protocol (the "sparse+" scheme prefix) is
// required here, not optional -- the Forwarder speaks plain HTTP, and
// Cargo's legacy git-based index protocol assumes a git-clonable index
// repo, which the Forwarder doesn't serve.
func CargoConfigTOML(port int) string {
	return fmt.Sprintf(`[source.crates-io]
replace-with = "spindrift-registry-proxy"

[source.spindrift-registry-proxy]
registry = "sparse+http://127.0.0.1:%d/"
`, port)
}
