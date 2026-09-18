package runner

import (
	"errors"
	"strings"
	"testing"
)

func TestBinaryFor_Rancher(t *testing.T) {
	if got := BinaryFor("rancher"); got != "nerdctl" {
		t.Errorf("BinaryFor(%q) = %q, want %q", "rancher", got, "nerdctl")
	}
}

func TestBinaryFor_Identity(t *testing.T) {
	if got := BinaryFor("podman"); got != "podman" {
		t.Errorf("BinaryFor(%q) = %q, want %q", "podman", got, "podman")
	}
}

func TestAliasFor_Nerdctl(t *testing.T) {
	if got := AliasFor("nerdctl"); got != "rancher" {
		t.Errorf("AliasFor(%q) = %q, want %q", "nerdctl", got, "rancher")
	}
}

func TestAliasFor_Identity(t *testing.T) {
	if got := AliasFor("podman"); got != "podman" {
		t.Errorf("AliasFor(%q) = %q, want %q", "podman", got, "podman")
	}
}

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

// A runtime added to lib/runtime-values.nix must resolve through BinaryFor
// to a binary that Precedence probes, or the prompt offers a runtime Probe
// never detects.
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

// AliasFor resolves the reverse direction by scanning map values, so two
// operator values sharing a binary would make its result depend on map
// iteration order.
func TestRuntimeAliases_NoAmbiguousReverseMapping(t *testing.T) {
	seen := map[string]string{}
	for runtime, bin := range runtimeAliases {
		if other, ok := seen[bin]; ok {
			t.Errorf("both %q and %q alias to binary %q — AliasFor(%q) would be ambiguous", runtime, other, bin, bin)
		}
		seen[bin] = runtime
	}
}

// The wanted substrings are the same ones quickstart's own
// TestRunQuickstart_NoRuntimeDetected_ReturnsActionableError checks, so
// dropping a runtime name from the error breaks both tests together.
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
