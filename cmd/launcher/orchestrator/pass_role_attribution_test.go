package main

import (
	"testing"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/passmachine"
)

// TestPassRoleAttributionCoversEveryPassKind guards against drift (issue
// #4425): the claude driver cannot import passmachine, so its role literals
// are hand-kept and nothing else fails when a new PassKind goes unmapped.
func TestPassRoleAttributionCoversEveryPassKind(t *testing.T) {
	// Assumes KindLegacy is the only role-less kind and sits before
	// KindImplement, and that the first "" String() past it ends the enum.
	for k := passmachine.KindImplement; k.String() != ""; k++ {
		if claude.AttributionRoleForPass(k.String()) == "" {
			t.Errorf("pass kind %q has no attribution role; add it to claude.AttributionRoleForPass", k.String())
		}
	}
}
