package ecosystem

import (
	"fmt"

	"spindrift.dev/launcher/internal/registrymanifest"
)

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
// repo, which the Forwarder doesn't serve. prefix is the manifest route this
// config binds to -- see runBindRegistryBindings in
// cmd/launcher/driver-exec/bindregistry_cmd.go for why it's always the
// first manifest route's prefix. routes is accepted (and ignored) only to
// satisfy HomeConfigRenderer's signature (issue #3259): this is cargo's
// pre-clone *base* template, unrelated to cargo's real host-rooted logic,
// which lives entirely in the post-clone CargoRepoAwareConfig/
// CargoConfigTOMLWithReplacements path.
func CargoConfigTOML(port int, prefix string, routes []registrymanifest.Route) string {
	return fmt.Sprintf(`[source.crates-io]
replace-with = "spindrift-registry-proxy"

[source.spindrift-registry-proxy]
registry = "sparse+http://127.0.0.1:%d/%s/"
`, port, prefix)
}
