package main

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/passmachine"
)

// TestPassRoleAttributionCoversEveryPassKind guards against drift (issue
// #4425): dispatchkind is a leaf and cannot import passmachine, so its
// PassRoles keys are hand-kept and nothing else fails when a new PassKind goes
// unmapped.
func TestPassRoleAttributionCoversEveryPassKind(t *testing.T) {
	// Assumes KindLegacy is the only role-less kind and sits before
	// KindImplement, and that the first "" String() past it ends the enum.
	for _, d := range dispatchkind.All {
		for k := passmachine.KindImplement; k.String() != ""; k++ {
			if d.PassRoles[k.String()] == "" {
				t.Errorf("%s: pass kind %q has no attribution role; add it to dispatchkind's passRoles", d.Name, k.String())
			}
		}
	}
}
