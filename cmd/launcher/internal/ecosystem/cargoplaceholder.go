package ecosystem

import (
	"regexp"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// cargoBareKeyPattern matches cargo/TOML's own bare-key charset -- letters,
// digits, "-", and "_". A quoted [registries."..."] table name can otherwise
// carry arbitrary single-line text (spaces, ";", backticks, "$(...)", ...);
// since that text flows unquoted (as a shell variable name, not just a
// value) into driver-exec's rendered env-export file that entrypoint.sh
// sources, any name failing this check must never reach a caller.
var cargoBareKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// CargoPlaceholderToken is the fixed, non-secret value emitted for every
// cargo source replacement bound to the Forwarder, keyed to the replacement
// proxy source cargo looks credentials up against (ADR 0044's issue #3053
// amendment, re-keyed by issue #3201's source replacement). cargo's
// client-side credential lookup (cargo:token) aborts before the Forwarder is
// ever contacted unless something satisfies it locally; this placeholder
// exists only to satisfy that local check; the Box->Forwarder hop stays
// unauthenticated, and the Forwarder's Rewrite hook replaces the
// Authorization header on the Forwarder->upstream hop with the real
// credential regardless of what arrives here. The value is fixed and
// self-documenting so that leaking it in a log is visibly harmless.
const CargoPlaceholderToken = "spindrift-registry-proxy-placeholder-not-a-secret"

// CargoRegistryEnvVarName renders registryName into the env var name cargo's
// own credential-provider machinery reads for it: CARGO_REGISTRIES_<NAME>_TOKEN,
// with NAME uppercased and "-" mapped to "_" -- cargo's own convention for
// turning a [registries.NAME] table name into an env var.
func CargoRegistryEnvVarName(registryName string) string {
	upper := strings.ToUpper(registryName)
	upper = strings.ReplaceAll(upper, "-", "_")
	return "CARGO_REGISTRIES_" + upper + "_TOKEN"
}

// RouteLocalURL renders route's own local Forwarder URL: the proxy listens
// on one port for every route, but each route answers only its own
// prefix-scoped path (issue #3142), so the in-tree rewrite target has to
// carry that prefix too, not just the bare "http://127.0.0.1:<port>".
//
// Exported because a caller outside this package needs the same value to
// build its own host-rewrite records for a route that survived upstream-host
// collision filtering, a step this package has no reason to know about.
func RouteLocalURL(route registrymanifest.Route, port int) string {
	return "http://127.0.0.1:" + strconv.Itoa(port) + "/" + route.Prefix
}
