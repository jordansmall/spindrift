package ecosystem

import (
	"fmt"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// GoBindingInput is the subset of the process environment ComputeGoBindings
// reads to decide what to override and what to warn about. Passed in
// explicitly, rather than read via os.Getenv inside ComputeGoBindings
// itself, so the decision logic stays a pure function the tests can drive
// with plain struct literals -- no os.Setenv/os.Unsetenv bookkeeping needed
// to keep test cases hermetic and independent of each other.
type GoBindingInput struct {
	GOTOOLCHAIN string
	GONOPROXY   string
	GOPRIVATE   string
	GOSUMDB     string
	GONOSUMDB   string
}

// GoBindings is the computed result of ComputeGoBindings: which env vars to
// export (name/value pairs, in a stable order) and which warning lines to
// emit (verbatim wording ported from the deleted entrypoint.sh
// phase_go_binding, see git history) for a given forwarder port and
// environment snapshot.
type GoBindings struct {
	Exports  []EnvExport
	Warnings []string
}

// ComputeGoBindings points Go's own module-fetch tooling at the local
// Forwarder on 127.0.0.1:port: pins GOTOOLCHAIN=local, forces GONOPROXY=none,
// and (absent a GOPRIVATE/GONOSUMDB exemption) forces GOSUMDB=off --
// warning wherever a prior value is being overridden. This ports the
// decision logic and warning wording of the deleted entrypoint.sh
// phase_go_binding (see git history) verbatim; the rationale behind each
// override is inlined above the relevant branch below. GOPROXY's own
// decision is route-aware (issue #3260), and GONOPROXY/GOSUMDB ride along
// with it: both exist only to keep traffic on the Forwarder's controlled
// mirror, so when no GOPROXY export is rendered they would instead widen
// what reaches Go's own default public proxy. GOTOOLCHAIN=local is the one
// unconditional override -- it states a fact about this Box's baked
// toolchain, which holds whether or not anything is routed. See
// runBindRegistryBindings in cmd/launcher/driver-exec/bindregistry_cmd.go
// for why routes[0] is always the manifest route these bindings point at.
//
// GOPROXY mirrors NpmFamilyBindings' own decision (see its doc for the full
// rationale): routes[0].EnforcedPaths is searched for a "go"-tagged entry.
// Zero matches means the route declares no Go registry at all, so GOPROXY
// is left entirely unexported -- not a bare route-root URL, which would
// silently point Go at an upstream index that was never declared for it; a
// match renders the full-path URL, no trailing slash (unlike npm's three
// vars, GOPROXY takes none).
func ComputeGoBindings(port int, prefix string, routes []registrymanifest.Route, env GoBindingInput) GoBindings {
	var result GoBindings

	route := firstRoute(routes)
	goProxyBound := false
	// go-path is a single operator-declared string, not a discovery scan
	// that could produce duplicates (unlike npm's 0/1/>1 EnforcedPaths case
	// in NpmFamilyBindings) -- at most one "go"-tagged entry can ever appear
	// here, so no ambiguity handling is needed. Finding none leaves GOPROXY
	// unexported, mirroring NpmFamilyBindings' own zero-match fallback.
	for _, p := range route.EnforcedPaths {
		if p.Ecosystem == "go" {
			// go-path parse validation rejects a bare "/"
			// (registryroutes.go's validateDeclaredPath), so unlike npm's
			// whole-host case this match can never normalize to "" --
			// concatenated as-is.
			result.Exports = append(result.Exports, EnvExport{Name: "GOPROXY", Value: fmt.Sprintf("http://127.0.0.1:%d/%s%s", port, prefix, p.Path)})
			goProxyBound = true
			break
		}
	}

	// Pin GOTOOLCHAIN=local so the default GOTOOLCHAIN=auto never triggers a
	// toolchain switch: Go's own useSumDB forces a checksum-database lookup
	// for golang.org/toolchain even when GOSUMDB=off, so a Target repo
	// naming a newer toolchain would otherwise die on a checksum failure
	// that looks like tampering, not a version mismatch. This Box only ever
	// offers one baked Go toolchain through the Forwarder anyway, so a repo
	// needing a newer one gets Go's own clear "go.mod requires go >= X.Y.Z"
	// error instead. Unlike the two overrides below, this one holds whether
	// or not a GOPROXY export was rendered: it describes what this Box
	// carries, not where module traffic goes.
	if env.GOTOOLCHAIN != "" && env.GOTOOLCHAIN != "local" {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"==> WARNING: overriding GOTOOLCHAIN=%s with GOTOOLCHAIN=local — this Box only offers one baked Go toolchain through the Forwarder",
			env.GOTOOLCHAIN,
		))
	}
	result.Exports = append(result.Exports, EnvExport{Name: "GOTOOLCHAIN", Value: "local"})

	// A repo-set GOPRIVATE defaults GONOPROXY to itself too, routing those
	// paths' fetches straight to the internet, bypassing GOPROXY entirely --
	// and Go's own cfg.envOr("GONOPROXY", GOPRIVATE) treats unset and
	// empty-string GONOPROXY identically, both falling back to GOPRIVATE, so
	// a plain empty string here would NOT close that bypass. "none" is the
	// documented sentinel for "matches nothing" (see `go help private`) and
	// is the only value that forces every module path, private or not,
	// through the Forwarder. GOPRIVATE's other default effect (also
	// defaulting GONOSUMDB, exempting private paths from the public
	// checksum database) is left untouched. Gated on GOPROXY actually
	// being bound: with Go falling back to its own default public proxy,
	// "none" would push a repo's GOPRIVATE paths out to that proxy rather
	// than fetching them direct -- the opposite of closing a bypass.
	if goProxyBound {
		if env.GONOPROXY != "" || env.GOPRIVATE != "" {
			result.Warnings = append(result.Warnings, "==> WARNING: overriding pre-existing GONOPROXY/GOPRIVATE with GONOPROXY=none — every module path, private or not, now routes through the Forwarder")
		}
		result.Exports = append(result.Exports, EnvExport{Name: "GONOPROXY", Value: "none"})
	}

	// A single-upstream mirror has no way to know which module paths under
	// that upstream are actually private without the repo declaring an
	// exemption, and a checksum-database lookup for a module that turns out
	// to be private is exactly the leak this binding exists to prevent --
	// go.sum's own committed hashes remain the primary integrity check, this
	// only forgoes the live database lookup. Keyed on the two ways a repo
	// can declare an exemption: if GOPRIVATE is set, Go's own default
	// already derives GONOSUMDB from it, so this branch doesn't fire; if
	// GONOSUMDB is set explicitly, the repo has taken responsibility for
	// what's exempted, so again GOSUMDB is left alone. With neither
	// exemption declared, this deliberately overrides even an explicit
	// repo-set GOSUMDB -- there is no other way to guarantee a private path
	// never reaches it. Gated on GOPROXY actually being bound for the same
	// reason as GONOPROXY above: the leak this forgoes a lookup to prevent
	// only exists while fetches go through the single-upstream mirror.
	if goProxyBound && env.GOPRIVATE == "" && env.GONOSUMDB == "" {
		if env.GOSUMDB != "" {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"==> WARNING: overriding explicit GOSUMDB=%s with GOSUMDB=off — no GOPRIVATE/GONOSUMDB exemption declared",
				env.GOSUMDB,
			))
		}
		result.Exports = append(result.Exports, EnvExport{Name: "GOSUMDB", Value: "off"})
	}

	return result
}
