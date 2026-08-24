package runner

import (
	"errors"
	"strings"
	"testing"
)

// TestBinaryFor_Rancher verifies BinaryFor maps the operator-facing "rancher"
// value to the "nerdctl" CLI binary it actually invokes.
func TestBinaryFor_Rancher(t *testing.T) {
	if got := BinaryFor("rancher"); got != "nerdctl" {
		t.Errorf("BinaryFor(%q) = %q, want %q", "rancher", got, "nerdctl")
	}
}

// TestBinaryFor_Identity verifies BinaryFor returns any non-aliased runtime
// value unchanged.
func TestBinaryFor_Identity(t *testing.T) {
	if got := BinaryFor("podman"); got != "podman" {
		t.Errorf("BinaryFor(%q) = %q, want %q", "podman", got, "podman")
	}
}

// TestAliasFor_Nerdctl verifies AliasFor maps the "nerdctl" binary back to
// the operator-facing "rancher" value.
func TestAliasFor_Nerdctl(t *testing.T) {
	if got := AliasFor("nerdctl"); got != "rancher" {
		t.Errorf("AliasFor(%q) = %q, want %q", "nerdctl", got, "rancher")
	}
}

// TestAliasFor_Identity verifies AliasFor returns any non-aliased binary name
// unchanged.
func TestAliasFor_Identity(t *testing.T) {
	if got := AliasFor("podman"); got != "podman" {
		t.Errorf("AliasFor(%q) = %q, want %q", "podman", got, "podman")
	}
}

// TestProbe_PrecedenceOrder verifies Probe returns the highest-precedence
// runtime when more than one is present on PATH.
func TestProbe_PrecedenceOrder(t *testing.T) {
	present := map[string]bool{"podman": true, "docker": true}
	lookPath := func(bin string) (string, error) {
		if present[bin] {
			return "/usr/bin/" + bin, nil
		}
		return "", errors.New("not found")
	}
	got, err := Probe(lookPath)
	if err != nil {
		t.Fatalf("Probe() error = %v, want nil", err)
	}
	if got != "podman" {
		t.Errorf("Probe() = %q, want %q (higher precedence than docker)", got, "podman")
	}
}

// TestProbe_NerdctlAliasedToRancher verifies Probe returns the aliased
// operator-facing value "rancher" when only nerdctl is present on PATH.
func TestProbe_NerdctlAliasedToRancher(t *testing.T) {
	lookPath := func(bin string) (string, error) {
		if bin == "nerdctl" {
			return "/usr/bin/nerdctl", nil
		}
		return "", errors.New("not found")
	}
	got, err := Probe(lookPath)
	if err != nil {
		t.Fatalf("Probe() error = %v, want nil", err)
	}
	if got != "rancher" {
		t.Errorf("Probe() = %q, want %q", got, "rancher")
	}
}

// TestPrecedence_CoversValidValues verifies every ValidValues entry resolves
// (via BinaryFor) to a binary name Precedence actually probes — guarding
// against a runtime added to lib/runtime-values.nix that the prompt offers
// but Probe never detects.
func TestPrecedence_CoversValidValues(t *testing.T) {
	for _, v := range ValidValues {
		bin := BinaryFor(v)
		found := false
		for _, p := range Precedence {
			if p == bin {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ValidValues %q (binary %q) not present in Precedence %v", v, bin, Precedence)
		}
	}
}

// TestRuntimeAliases_NoAmbiguousReverseMapping verifies runtimeAliases never
// maps two operator values to the same binary — AliasFor resolves the
// reverse direction by scanning map values, so two entries sharing a binary
// would make its result map-iteration-order-dependent.
func TestRuntimeAliases_NoAmbiguousReverseMapping(t *testing.T) {
	seen := map[string]string{}
	for runtime, bin := range runtimeAliases {
		if other, ok := seen[bin]; ok {
			t.Errorf("both %q and %q alias to binary %q — AliasFor(%q) would be ambiguous", runtime, other, bin, bin)
		}
		seen[bin] = runtime
	}
}

// TestProbe_NoneFound_ReturnsActionableError verifies Probe returns an
// actionable error naming every supported runtime when none is found on
// PATH — the same substrings quickstart's own
// TestRunQuickstart_NoRuntimeDetected_ReturnsActionableError checks.
func TestProbe_NoneFound_ReturnsActionableError(t *testing.T) {
	lookPath := func(bin string) (string, error) {
		return "", errors.New("not found")
	}
	_, err := Probe(lookPath)
	if err == nil {
		t.Fatal("Probe() should error when no runtime is found")
	}
	for _, want := range []string{"podman", "docker", "rancher", "bwrap"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err.Error(), want)
		}
	}
}
