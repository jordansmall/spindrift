package bindregistry

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrymanifest"
)

// Blocking review finding on issue #3142: two routes naming the same UpstreamHost
// (legal, e.g. one Artifactory host fronting separate npm and cargo prefixes)
// cannot be told apart by ApplyInTreeBinding's host-only content match, so every
// rewrite for the shared host is dropped rather than the first kept, while a
// third route on a distinct host survives untouched.
func TestBuildIntreeHostRewrites_DuplicateUpstreamHostDropsBothAndReportsCollision(t *testing.T) {
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "shared.example"},
		{Prefix: "r1", UpstreamHost: "shared.example"},
		{Prefix: "r2", UpstreamHost: "distinct.example"},
	}

	rewrites, collisions := buildIntreeHostRewrites(routes, 9999)

	if len(rewrites) != 1 || rewrites[0].UpstreamHost != "distinct.example" {
		t.Errorf("rewrites = %+v, want exactly the distinct.example route surviving", rewrites)
	}
	if len(collisions) != 1 {
		t.Fatalf("collisions = %+v, want exactly one collision entry", collisions)
	}
	if collisions[0].Host != "shared.example" {
		t.Errorf("collisions[0].Host = %q, want %q", collisions[0].Host, "shared.example")
	}
	if got := strings.Join(collisions[0].Prefixes, ","); got != "r0,r1" {
		t.Errorf("collisions[0].Prefixes = %v, want [r0 r1] in table order", collisions[0].Prefixes)
	}
}

// Issue #3706: hosts differing only in case are one logical host, since
// ApplyInTreeBinding's own duplicate guard folds case; they collide exactly like
// an identical host, reporting the first-seen spelling.
func TestBuildIntreeHostRewrites_CaseVariantUpstreamHostsCollide(t *testing.T) {
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "artifactory.example.test"},
		{Prefix: "r1", UpstreamHost: "ARTIFACTORY.example.test"},
		{Prefix: "r2", UpstreamHost: "distinct.example"},
	}

	rewrites, collisions := buildIntreeHostRewrites(routes, 9999)

	if len(rewrites) != 1 || rewrites[0].UpstreamHost != "distinct.example" {
		t.Errorf("rewrites = %+v, want exactly the distinct.example route surviving", rewrites)
	}
	if len(collisions) != 1 {
		t.Fatalf("collisions = %+v, want exactly one collision entry", collisions)
	}
	if collisions[0].Host != "artifactory.example.test" {
		t.Errorf("collisions[0].Host = %q, want first-seen spelling %q", collisions[0].Host, "artifactory.example.test")
	}
	if got := strings.Join(collisions[0].Prefixes, ","); got != "r0,r1" {
		t.Errorf("collisions[0].Prefixes = %v, want [r0 r1] in table order", collisions[0].Prefixes)
	}
}

// A port is part of the host identity: host and host:8443 stay distinct.
func TestBuildIntreeHostRewrites_HostAndHostWithPortDoNotCollide(t *testing.T) {
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "artifactory.example.test"},
		{Prefix: "r1", UpstreamHost: "ARTIFACTORY.example.test:8443"},
	}

	rewrites, collisions := buildIntreeHostRewrites(routes, 9999)

	if len(rewrites) != 2 || len(collisions) != 0 {
		t.Errorf("rewrites = %+v, collisions = %+v, want two rewrites and no collision", rewrites, collisions)
	}
}

// Non-blocking review finding on issue #3142: rewriteHostNames must not repeat a
// host appearing in more than one rewrite, and must preserve first-occurrence
// order rather than sorting.
func TestRewriteHostNames_DedupesPreservingFirstOccurrenceOrder(t *testing.T) {
	rewrites := []HostRewrite{
		{UpstreamHost: "a.example", LocalURL: "http://127.0.0.1:1/a"},
		{UpstreamHost: "b.example", LocalURL: "http://127.0.0.1:1/b"},
		{UpstreamHost: "a.example", LocalURL: "http://127.0.0.1:1/a2"},
	}

	if got, want := rewriteHostNames(rewrites), "a.example, b.example"; got != want {
		t.Errorf("rewriteHostNames = %q, want %q", got, want)
	}
}

// Both halves of the summary's skip rule: a BindingEnvVar row is named only when
// the rendered exports carry its var, while a HomeConfig row is named
// unconditionally because its file is always written.
func TestBindingSummaryProse_SkipsUnrenderedBindingEnvVars(t *testing.T) {
	original := ecosystem.Table
	ecosystem.Table = []ecosystem.Row{
		{Name: "stub-bound", BindingEnvVar: "STUB_BOUND_REGISTRY"},
		{Name: "stub-unbound", BindingEnvVar: "STUB_UNBOUND_REGISTRY"},
		{Name: "stub-file", HomeConfig: &ecosystem.HomeConfig{}},
	}
	t.Cleanup(func() { ecosystem.Table = original })

	got := bindingSummaryProse(
		[]ecosystem.EnvExport{{Name: "STUB_BOUND_REGISTRY", Value: "http://127.0.0.1:1/r0"}},
		map[string]string{"stub-file": "/tmp/stub.conf"},
	)
	want := "stub-bound bound to it via STUB_BOUND_REGISTRY and stub-file bound to it via /tmp/stub.conf"
	if got != want {
		t.Errorf("bindingSummaryProse = %q, want %q", got, want)
	}
}

// The degenerate case the skip rule newly makes reachable: empty prose is what
// lets the caller drop the separator rather than print a dangling one.
func TestBindingSummaryProse_EmptyWhenNothingBound(t *testing.T) {
	original := ecosystem.Table
	ecosystem.Table = []ecosystem.Row{{Name: "stub-unbound", BindingEnvVar: "STUB_UNBOUND_REGISTRY"}}
	t.Cleanup(func() { ecosystem.Table = original })

	if got := bindingSummaryProse(nil, nil); got != "" {
		t.Errorf("bindingSummaryProse(nothing bound) = %q, want %q", got, "")
	}
}

// joinProse's three cardinalities: one item bare, two joined on a bare "and" with
// no comma, three or more with an Oxford comma. Both bindings-mode fallback
// warnings and the repo-aware no-route warning rely on this shape to read as
// operator prose rather than a raw comma-joined dump.
func TestJoinProse(t *testing.T) {
	if got, want := joinProse([]string{"cargo"}), "cargo"; got != want {
		t.Errorf("joinProse(1 item) = %q, want %q", got, want)
	}
	if got, want := joinProse([]string{"cargo", "npm"}), "cargo and npm"; got != want {
		t.Errorf("joinProse(2 items) = %q, want %q", got, want)
	}
	if got, want := joinProse([]string{"cargo", "npm", "yarn"}), "cargo, npm, and yarn"; got != want {
		t.Errorf("joinProse(3 items) = %q, want %q", got, want)
	}
}

// Reviewer finding on issue #3142: a route whose UpstreamHost
// buildIntreeHostRewrites already dropped as a collision must not survive
// dropCollidedRoutes, since nothing on disk was ever rewritten to that route's
// LocalURL. Cargo's exports-side analogue is covered by ecosystem's own package
// tests (issue #3201).
func TestDropCollidedRoutes_SkipsRouteWithCollidedUpstreamHost(t *testing.T) {
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "shared.example", Ecosystems: ecosystem.CargoRouteBlock("collided-one")},
		{Prefix: "r1", UpstreamHost: "shared.example", Ecosystems: ecosystem.CargoRouteBlock("collided-two")},
		{Prefix: "r2", UpstreamHost: "distinct.example", Ecosystems: ecosystem.CargoRouteBlock("valid-registry")},
	}
	_, collisions := buildIntreeHostRewrites(routes, 9999)

	filtered := dropCollidedRoutes(routes, collisions)

	if len(filtered) != 1 || filtered[0].Prefix != "r2" {
		t.Errorf("filtered = %+v, want exactly one surviving route (r2/distinct.example)", filtered)
	}
}

// Issue #3706: a case-variant twin of a collided host is the same host, so its
// route must be dropped too.
func TestDropCollidedRoutes_SkipsCaseVariantOfCollidedUpstreamHost(t *testing.T) {
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "artifactory.example.test", Ecosystems: ecosystem.CargoRouteBlock("collided-one")},
		{Prefix: "r1", UpstreamHost: "ARTIFACTORY.example.test", Ecosystems: ecosystem.CargoRouteBlock("collided-two")},
		{Prefix: "r2", UpstreamHost: "distinct.example", Ecosystems: ecosystem.CargoRouteBlock("valid-registry")},
	}
	_, collisions := buildIntreeHostRewrites(routes, 9999)

	filtered := dropCollidedRoutes(routes, collisions)

	if len(filtered) != 1 || filtered[0].Prefix != "r2" {
		t.Errorf("filtered = %+v, want exactly one surviving route (r2/distinct.example)", filtered)
	}
}
