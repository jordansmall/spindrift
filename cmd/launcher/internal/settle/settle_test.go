package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/forge"
)

// TestNew_ImplementsSettler asserts that the concrete type New returns
// satisfies Settler, the seam callers depend on so tests can inject a Fake.
func TestNew_ImplementsSettler(t *testing.T) {
	fc := forge.NewFake()
	var _ Settler = New(Config{}, fc, fc)
}

// TestNew_ReadsPRForgeAndLandingRecorderFromConfigCapabilities asserts New's
// pr/landing fields are a strict read of cfg.Capabilities (issue #2945),
// never re-derived from cf/it via its own type assertion: a mismatched
// (zero-value) Capabilities yields nil pr/landing even though the cf/it
// fakes handed to New do implement forge.PRForge/forge.LandingRecorder.
func TestNew_ReadsPRForgeAndLandingRecorderFromConfigCapabilities(t *testing.T) {
	fc := forge.NewFake()
	cf := fc.AsGithubReadOnly()
	it := fc.AsLocalShaped()

	got := New(Config{Capabilities: forge.Capabilities{}}, it, cf)
	if got.pr != nil {
		t.Errorf("pr = %v, want nil when Config.Capabilities is the zero value, regardless of what cf implements", got.pr)
	}
	if got.landing != nil {
		t.Errorf("landing = %v, want nil when Config.Capabilities is the zero value, regardless of what it implements", got.landing)
	}

	caps := forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
	got = New(Config{Capabilities: caps}, it, cf)
	if got.pr == nil {
		t.Errorf("pr = nil, want non-nil when Config.Capabilities was resolved from cf/it")
	}
	if got.landing == nil {
		t.Errorf("landing = nil, want non-nil when Config.Capabilities was resolved from cf/it")
	}
}

// newTestSettle constructs a Settle for tests that don't care about
// capability-resolution mechanics: it derives cfg.Capabilities from it/cf
// via the same forge.ResolveCapabilities production code path uses, so
// existing tests keep asserting on pr/landing behavior for whatever fake
// shape they already pass, without each test wiring Capabilities by hand.
func newTestSettle(cfg Config, it forge.IssueTracker, cf forge.CodeForge) *Settle {
	cfg.Capabilities = forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
	return New(cfg, it, cf)
}
