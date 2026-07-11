package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestNew_ImplementsSettler asserts that the concrete type New returns
// satisfies Settler, the seam callers depend on so tests can inject a Fake.
func TestNew_ImplementsSettler(t *testing.T) {
	fc := forge.NewFake()
	var _ Settler = New(Config{}, fc, fc)
}
