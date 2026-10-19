package backendkit

import (
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
)

func reset(t *testing.T) {
	t.Helper()
	saved := registry
	registry = nil
	t.Cleanup(func() { registry = saved })
}

func strategy(name string) Strategy {
	return Strategy{Descriptor: backend.Descriptor{Name: name}}
}

func TestRegisterThenByNameRoundTrips(t *testing.T) {
	reset(t)
	Register(strategy("alpha"))
	got, ok := ByName("alpha")
	if !ok || got.Name != "alpha" {
		t.Fatalf("ByName(alpha) = %q, %v; want alpha, true", got.Name, ok)
	}
}

func TestRegisterDuplicatePanicsNamingBackend(t *testing.T) {
	reset(t)
	Register(strategy("alpha"))
	defer func() {
		msg, _ := recover().(string)
		if !strings.Contains(msg, "alpha") {
			t.Fatalf("panic = %q, want one naming alpha", msg)
		}
	}()
	Register(strategy("alpha"))
	t.Fatal("duplicate Register did not panic")
}

func TestByNameUnregisteredIsNotOK(t *testing.T) {
	reset(t)
	if _, ok := ByName("missing"); ok {
		t.Fatal("ByName(missing) ok = true, want false")
	}
}

func TestStrategiesInRegistrationOrder(t *testing.T) {
	reset(t)
	want := []string{"zeta", "alpha", "mid"}
	for _, n := range want {
		Register(strategy(n))
	}
	var got []string
	for _, s := range Strategies() {
		got = append(got, s.Name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Strategies() names = %v, want %v", got, want)
	}
}

func TestStrategiesReturnsACopy(t *testing.T) {
	reset(t)
	Register(strategy("alpha"))
	Strategies()[0] = strategy("clobbered")
	if s, ok := ByName("alpha"); !ok || s.Name != "alpha" {
		t.Fatalf("mutating Strategies() result changed the registry")
	}
}
