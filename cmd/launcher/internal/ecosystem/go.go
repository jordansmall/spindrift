package ecosystem

import (
	"fmt"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// nameGo lets ComputeGoBindings name the ecosystem without reading goRow.Name,
// which would make goRow depend on itself: its EnvExports closure calls
// ComputeGoBindings, so the reference back closes an initialization cycle.
const nameGo = "go"

// GoRetiredRouteKey is go's Row.RetiredRouteKey. Exported so registryroutes,
// which still translates the retired spelling, never spells the key itself.
const GoRetiredRouteKey = "go-path"

// goRow is the go ecosystem's Table entry. envExportOrderGo pins GOPROXY ahead
// of the npm-family vars in the rendered export file, independent of goRow's
// position in Table's own precedence order.
var goRow = Row{
	Name:            nameGo,
	RetiredRouteKey: GoRetiredRouteKey,
	LockfileNames:   []string{"go.sum"},
	Classification:  "go mod",
	EnvExports: func(port int, prefix string, getenv func(string) string, routes []registrymanifest.Route) ([]EnvExport, []string) {
		result := ComputeGoBindings(port, prefix, routes, GoBindingInput{
			GOTOOLCHAIN: getenv("GOTOOLCHAIN"),
			GONOPROXY:   getenv("GONOPROXY"),
			GOPRIVATE:   getenv("GOPRIVATE"),
			GOSUMDB:     getenv("GOSUMDB"),
			GONOSUMDB:   getenv("GONOSUMDB"),
		})
		return result.Exports, result.Warnings
	},
	EnvExportOrder: envExportOrderGo,
	BindingEnvVar:  "GOPROXY",
}

// GoBindingInput is the environment subset ComputeGoBindings reads. The caller
// passes it in rather than ComputeGoBindings calling os.Getenv, which keeps the
// decision logic a pure function tests drive with struct literals.
type GoBindingInput struct {
	GOTOOLCHAIN string
	GONOPROXY   string
	GOPRIVATE   string
	GOSUMDB     string
	GONOSUMDB   string
}

// GoBindings is what ComputeGoBindings decided: the env vars to export, in a
// stable order, and the warning lines to emit.
type GoBindings struct {
	Exports  []EnvExport
	Warnings []string
}

// ComputeGoBindings points Go's module-fetch tooling at the local Forwarder on
// 127.0.0.1:port. GOPROXY is route-aware (issue #3260): it renders only when
// routes[0]'s ecosystems block declares a go path (issue #3404), because a bare
// route-root URL would point Go at an upstream nothing declared for it. That
// export also gates GONOPROXY and GOSUMDB; GOTOOLCHAIN=local holds regardless.
func ComputeGoBindings(port int, prefix string, routes []registrymanifest.Route, env GoBindingInput) GoBindings {
	var result GoBindings

	goProxyBound := false
	// go-path parse validation rejects a bare "/" (registryroutes.go's
	// validateDeclaredPath), so unlike npm's whole-host case this path can never
	// normalize to "", and the URL below concatenates it as-is.
	if path := declaredPath(routes, nameGo); path != "" {
		result.Exports = append(result.Exports, EnvExport{Name: "GOPROXY", Value: fmt.Sprintf("http://127.0.0.1:%d/%s%s", port, prefix, path)})
		goProxyBound = true
	}

	// The default GOTOOLCHAIN=auto switches toolchains, and Go's useSumDB forces
	// a checksum-database lookup for golang.org/toolchain even under GOSUMDB=off,
	// so a repo naming a newer toolchain dies on what looks like tampering.
	// Pinning local produces Go's own "go.mod requires go >= X.Y.Z" error
	// instead, and holds whether or not a GOPROXY export was rendered.
	if env.GOTOOLCHAIN != "" && env.GOTOOLCHAIN != "local" {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"==> WARNING: overriding GOTOOLCHAIN=%s with GOTOOLCHAIN=local — this Box only offers one baked Go toolchain through the Forwarder",
			env.GOTOOLCHAIN,
		))
	}
	result.Exports = append(result.Exports, EnvExport{Name: "GOTOOLCHAIN", Value: "local"})

	// A repo-set GOPRIVATE defaults GONOPROXY to itself, bypassing GOPROXY, and
	// Go treats an empty GONOPROXY as unset, so "" does not close that bypass.
	// "none" is the documented sentinel matching nothing (`go help private`).
	// Without goProxyBound, "none" would push private paths to Go's own public
	// proxy instead of closing a bypass.
	if goProxyBound {
		if env.GONOPROXY != "" || env.GOPRIVATE != "" {
			result.Warnings = append(result.Warnings, "==> WARNING: overriding pre-existing GONOPROXY/GOPRIVATE with GONOPROXY=none — every module path, private or not, now routes through the Forwarder")
		}
		result.Exports = append(result.Exports, EnvExport{Name: "GONOPROXY", Value: "none"})
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
		result.Exports = append(result.Exports, EnvExport{Name: "GOSUMDB", Value: "off"})
	}

	return result
}
