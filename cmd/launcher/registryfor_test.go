package main

import (
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/settle"
)

// A recover gesture taken against a settler that is already running a session
// must reach the session's registry, not displace it: both handles mark and
// observe the same terminations (#3522).
func TestRegistryFor_ReturnsSettlersOwnRegistry(t *testing.T) {
	fc := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	s := settle.New(settle.Config{
		Capabilities: forge.ResolveCapabilities(fc, fc, backend.Descriptor{}, backend.Descriptor{}),
	}, fc, fc)

	session := registryFor(s)
	gen := session.Begin("42")
	recovered := registryFor(s)
	recovered.Mark("42")

	if !session.Marked("42", gen) {
		t.Error("session registry: want #42 marked through the recover handle")
	}
	if !s.Registry().Marked("42", gen) {
		t.Error("settler registry: want #42 marked")
	}
}

// A settler with no registry of its own (settle.Fake, ResearchSettle) still
// gets a usable registry, so the shutdown gate never dereferences a nil one.
func TestRegistryFor_NonRegistrarGetsFreshRegistry(t *testing.T) {
	if got := registryFor(struct{}{}); got == nil {
		t.Error("registryFor(non-Registrar) = nil, want a fresh registry")
	}
}
