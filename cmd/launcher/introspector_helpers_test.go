package main

import (
	"testing"

	"spindrift.dev/launcher/internal/backendkit"
)

// withIntrospector returns a copy of ss with name's IntrospectToken hook
// replaced by f; nil models a backend with no hook.
func withIntrospector(ss []backendkit.Strategy, name string, f backendkit.TokenIntrospector) []backendkit.Strategy {
	out := append([]backendkit.Strategy{}, ss...)
	for i := range out {
		if out[i].Name == name {
			out[i].IntrospectToken = f
		}
	}
	return out
}

// swapIntrospector installs f as name's IntrospectToken hook in the registry
// for the test and restores the registry on cleanup.
func swapIntrospector(t *testing.T, name string, f backendkit.TokenIntrospector) {
	t.Helper()
	t.Cleanup(backendkit.ReplaceRegistryForTest(withIntrospector(backendkit.Strategies(), name, f)))
}
