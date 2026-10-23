package gomod

import (
	"fmt"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystem/ecosystemkit"
	"spindrift.dev/launcher/internal/registrymanifest"
)

// nameGo lets computeBindings name the ecosystem without reading Strategy.Name,
// which would make Strategy depend on itself: its EnvExports closure calls
// computeBindings, so the reference back closes an initialization cycle.
const nameGo = "go"

// goToolchainLocal pins GOTOOLCHAIN to the one baked toolchain.
const goToolchainLocal = "local"

// Strategy keeps the Name "go" though the package is gomod: go is a keyword.
var Strategy = ecosystem.Strategy{
	Name:             nameGo,
	CredentialStores: []string{"netrc", "npmrc", "gradle-properties"},
	RetiredRouteKey:  ecosystem.GoRetiredRouteKey,
	LockfileNames:    []string{"go.sum"},
	Classification:   "go mod",
	EnvExports: func(port int, prefix string, getenv func(string) string, routes []registrymanifest.Route) ([]ecosystem.EnvExport, []string) {
		result := computeBindings(port, prefix, routes, bindingInput{
			GOTOOLCHAIN: getenv("GOTOOLCHAIN"),
			GONOPROXY:   getenv("GONOPROXY"),
			GOPRIVATE:   getenv("GOPRIVATE"),
			GOSUMDB:     getenv("GOSUMDB"),
			GONOSUMDB:   getenv("GONOSUMDB"),
		})
		return result.Exports, result.Warnings
	},
	BindingEnvVar:        "GOPROXY",
	AnnounceBindingValue: true,
}

// bindingInput is the environment subset computeBindings reads. The caller
// passes it in rather than computeBindings calling os.Getenv, which keeps the
// decision logic a pure function tests drive with struct literals.
type bindingInput struct {
	GOTOOLCHAIN string
	GONOPROXY   string
	GOPRIVATE   string
	GOSUMDB     string
	GONOSUMDB   string
}

// bindings is what computeBindings decided: the env vars to export, in a
// stable order, and the warning lines to emit.
type bindings struct {
	Exports  []ecosystem.EnvExport
	Warnings []string
}

// computeBindings points Go's module-fetch tooling at the local Forwarder on
// 127.0.0.1:port. GOPROXY is route-aware (issue #3260): it renders only when
// routes[0]'s ecosystems block declares a go path (issue #3404), because a bare
// route-root URL would point Go at an upstream nothing declared for it. That
// export also gates GONOPROXY and GOSUMDB; GOTOOLCHAIN=local holds regardless.
func computeBindings(port int, prefix string, routes []registrymanifest.Route, env bindingInput) bindings {
	var result bindings

	goProxyBound := false
	// Parse validation of the [routes.ecosystems.go] path key rejects a bare "/"
	// (registryroutes.go's validateDeclaredPath), so unlike npm's whole-host case
	// this path can never normalize to "", and the URL below concatenates it
	// as-is.
	if path := ecosystemkit.DeclaredPath(routes, nameGo); path != "" {
		result.Exports = append(result.Exports, ecosystem.EnvExport{Name: "GOPROXY", Value: fmt.Sprintf("http://127.0.0.1:%d/%s%s", port, prefix, path)})
		goProxyBound = true
	}

	// The default GOTOOLCHAIN=auto switches toolchains, and Go's useSumDB forces
	// a checksum-database lookup for golang.org/toolchain even under GOSUMDB=off,
	// so a repo naming a newer toolchain dies on what looks like tampering.
	// Pinning local produces Go's own "go.mod requires go >= X.Y.Z" error
	// instead, and holds whether or not a GOPROXY export was rendered.
	if env.GOTOOLCHAIN != "" && env.GOTOOLCHAIN != goToolchainLocal {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"==> WARNING: overriding GOTOOLCHAIN=%s with GOTOOLCHAIN=%s — this Box only offers one baked Go toolchain through the Forwarder",
			env.GOTOOLCHAIN, goToolchainLocal,
		))
	}
	result.Exports = append(result.Exports, ecosystem.EnvExport{Name: "GOTOOLCHAIN", Value: goToolchainLocal})

	// A repo-set GOPRIVATE defaults GONOPROXY to itself, bypassing GOPROXY, and
	// Go treats an empty GONOPROXY as unset, so "" does not close that bypass.
	// "none" is the documented sentinel matching nothing (`go help private`).
	// Without goProxyBound, "none" would push private paths to Go's own public
	// proxy instead of closing a bypass.
	if goProxyBound {
		if env.GONOPROXY != "" || env.GOPRIVATE != "" {
			result.Warnings = append(result.Warnings, "==> WARNING: overriding pre-existing GONOPROXY/GOPRIVATE with GONOPROXY=none — every module path, private or not, now routes through the Forwarder")
		}
		result.Exports = append(result.Exports, ecosystem.EnvExport{Name: "GONOPROXY", Value: "none"})
	}

	// A checksum-database lookup for a module that turns out to be private is
	// the leak this prevents; go.sum's committed hashes stay the integrity
	// check. Either exemption hands the repo responsibility (Go derives
	// GONOSUMDB from a set GOPRIVATE), so the branch skips them and otherwise
	// overrides even an explicit GOSUMDB. goProxyBound gates it as above.
	if goProxyBound && env.GOPRIVATE == "" && env.GONOSUMDB == "" {
		if env.GOSUMDB != "" {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"==> WARNING: overriding explicit GOSUMDB=%s with GOSUMDB=off — no GOPRIVATE/GONOSUMDB exemption declared",
				env.GOSUMDB,
			))
		}
		result.Exports = append(result.Exports, ecosystem.EnvExport{Name: "GOSUMDB", Value: "off"})
	}

	return result
}
