package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/forge"
)

// Callers hold the Settler interface rather than *Settle so tests can inject a
// fake. New's return type must keep satisfying it.
func TestNew_ImplementsSettler(t *testing.T) {
	fc := forge.NewFake()
	var _ Settler = New(Config{}, fc, fc)
}

// New reads pr/landing straight out of cfg.Capabilities (issue #2945) instead
// of re-deriving them from cf/it with its own type assertion. A zero-value
// Capabilities therefore yields nil pr/landing even though the fakes passed in
// do implement forge.PRForge and forge.LandingRecorder.
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

// newTestSettle derives cfg.Capabilities through the same
// forge.ResolveCapabilities call production code uses, so tests that only care
// about pr/landing behavior need not wire Capabilities by hand.
func newTestSettle(cfg Config, it forge.IssueTracker, cf forge.CodeForge) *Settle {
	cfg.Capabilities = forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
	return New(cfg, it, cf)
}
