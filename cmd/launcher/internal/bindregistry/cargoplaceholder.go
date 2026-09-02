package bindregistry

import (
	"regexp"
	"strings"
)

// cargoBareKeyPattern matches cargo/TOML's own bare-key charset -- letters,
// digits, "-", and "_". A quoted [registries."..."] table name can otherwise
// carry arbitrary single-line text (spaces, ";", backticks, "$(...)", ...);
// since that text flows unquoted (as a shell variable name, not just a
// value) into driver-exec's rendered env-export file that entrypoint.sh
// sources, any name failing this check must never reach a caller.
var cargoBareKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// CargoPlaceholderToken is the fixed, non-secret value emitted for every
// rewritten cargo registry (ADR 0044's issue #3053 amendment). cargo's
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

// isPrefixContinuationByte reports whether b could extend a route Prefix
// (registryproxy.isValidPrefix's own charset, [a-z0-9-]) -- used by
// referencesLocalURL to tell "this index line names localURL's own route"
// from "this index line names a different route whose Prefix happens to
// start with localURL's Prefix", e.g. route Prefix "a" vs "a-2": since "-"
// is itself a valid Prefix character, ".../a-2/index/" is not a boundary
// match for localURL ".../a".
func isPrefixContinuationByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '-'
}

// referencesLocalURL reports whether s contains localURL immediately
// followed by end-of-string or a byte that couldn't extend a route Prefix
// -- a plain strings.Contains would also true-positive on a *different*
// route's LocalURL that merely starts with this one's, since one route's
// Prefix (e.g. "a") can be a strict textual prefix of another's (e.g.
// "a-2").
func referencesLocalURL(s, localURL string) bool {
	for from := 0; ; {
		i := strings.Index(s[from:], localURL)
		if i == -1 {
			return false
		}
		end := from + i + len(localURL)
		if end == len(s) || !isPrefixContinuationByte(s[end]) {
			return true
		}
		from += i + 1
	}
}

// ParseCargoRegistryNames scans content -- the already-rewritten (post-
// ApplyInTreeBinding) text of a cargo .cargo/config.toml -- for every
// [registries.NAME] table whose body contains a line assigning `index` that
// references localURL (see referencesLocalURL), i.e. a registry whose index
// was actually rewritten to point at the Forwarder. [source.NAME] tables are
// deliberately excluded: only [registries.*] is what cargo's
// credential-provider/token mechanism applies to.
//
// This is a lightweight line-based scanner, matching intreebinding.go's own
// deliberate choice to do plain string operations over a TOML config rather
// than pull in a TOML parser -- config.toml here is a rewrite target, not a
// document this package needs to round-trip or validate.
//
// A section's body runs from its header line to the next "[...]" header line
// (of any table, not just [registries.*]) or EOF. Names are returned in the
// order their [registries.*] header appears in content; a name repeated
// across two headers (not valid TOML, but not this function's job to
// reject) is deduped, keeping its first occurrence's position.
//
// A section whose extracted name doesn't match cargoBareKeyPattern (e.g. a
// quoted TOML key like [registries."evil; rm -rf /"]) is skipped entirely,
// as if it weren't a [registries.*] table at all -- never added to names,
// never an error.
func ParseCargoRegistryNames(content, localURL string) []string {
	lines := strings.Split(content, "\n")

	var names []string
	seen := make(map[string]bool)

	inRegistriesSection := false
	sectionName := ""
	sectionRewritten := false

	flush := func() {
		if inRegistriesSection && sectionRewritten && !seen[sectionName] && cargoBareKeyPattern.MatchString(sectionName) {
			seen[sectionName] = true
			names = append(names, sectionName)
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			// New header: close out whatever section (if any) precedes it.
			flush()

			header := strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]")
			name, ok := strings.CutPrefix(header, "registries.")
			if ok {
				inRegistriesSection = true
				sectionName = name
				sectionRewritten = false
			} else {
				inRegistriesSection = false
				sectionName = ""
				sectionRewritten = false
			}
			continue
		}

		if inRegistriesSection && !sectionRewritten {
			key, _, ok := strings.Cut(trimmed, "=")
			if ok && strings.TrimSpace(key) == "index" && referencesLocalURL(trimmed, localURL) {
				sectionRewritten = true
			}
		}
	}
	flush()

	return names
}

// CargoRegistryPlaceholders renders registryNames -- in the given order --
// into one EnvExport per name, each carrying CargoPlaceholderToken as its
// value. Reuses the EnvExport channel bindings mode already renders and
// entrypoint.sh already sources (ADR 0044), rather than a new seam.
//
// A name failing cargoBareKeyPattern is skipped rather than exported: since
// issue #3142, a caller may source registryNames straight from the manifest's
// Route.CargoRegistries (launcher-minted, already validated at mint time)
// rather than always from ParseCargoRegistryNames' own internal check, so
// this is a cheap defense-in-depth guard against a value that somehow
// reaches here unvalidated ending up as a shell variable name in the
// rendered, sourced env file.
func CargoRegistryPlaceholders(registryNames []string) []EnvExport {
	exports := make([]EnvExport, 0, len(registryNames))
	for _, name := range registryNames {
		if !cargoBareKeyPattern.MatchString(name) {
			continue
		}
		exports = append(exports, EnvExport{
			Name:  CargoRegistryEnvVarName(name),
			Value: CargoPlaceholderToken,
		})
	}
	return exports
}
