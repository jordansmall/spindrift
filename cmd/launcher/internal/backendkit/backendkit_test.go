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

func withRemedy(name, remedy string) Strategy {
	s := strategy(name)
	s.AutoMergeRemedy = func(Shared) string { return remedy }
	return s
}

func TestAutoMergeForgesNamesOnlyRemedyBackendsInOrder(t *testing.T) {
	ss := []Strategy{withRemedy("b", "x"), strategy("plain"), withRemedy("a", "y")}
	if got := AutoMergeForges(ss); got != "b or a" {
		t.Fatalf("AutoMergeForges = %q; want %q", got, "b or a")
	}
	if got := AutoMergeForges([]Strategy{strategy("plain")}); got != "" {
		t.Fatalf("AutoMergeForges with no remedy backend = %q; want empty", got)
	}
}

func TestAutoMergeRemedyClause(t *testing.T) {
	if got := withRemedy("a", "do it").AutoMergeRemedyClause(Shared{}); got != " — do it" {
		t.Fatalf("clause = %q; want %q", got, " — do it")
	}
	if got := strategy("plain").AutoMergeRemedyClause(Shared{}); got != "" {
		t.Fatalf("clause without hook = %q; want empty", got)
	}
}

func TestAutoMergeRemedyClauseReadsShared(t *testing.T) {
	s := strategy("a")
	s.AutoMergeRemedy = func(sh Shared) string { return "use " + sh.MergeMethod }
	if got := s.AutoMergeRemedyClause(Shared{MergeMethod: "squash"}); got != " — use squash" {
		t.Fatalf("clause = %q", got)
	}
}
