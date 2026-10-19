package runner

import (
	"slices"
	"strings"
	"testing"
)

// isolateRegistry swaps in an empty registry for the test, so fake families
// never leak into other tests or into the bundle's real registrations.
func isolateRegistry(t *testing.T) {
	t.Helper()
	saved := strategies
	strategies = map[string]Strategy{}
	t.Cleanup(func() { strategies = saved })
}

func TestRegisterPanicsOnDuplicate(t *testing.T) {
	isolateRegistry(t)
	Register(Strategy{Name: "fake"})
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("second Register of the same name did not panic")
		}
		if msg, _ := r.(string); !strings.Contains(msg, "fake") {
			t.Errorf("panic %v does not name the duplicate family", r)
		}
	}()
	Register(Strategy{Name: "fake"})
}

func TestLookupUnknownKindNamesRegisteredFamilies(t *testing.T) {
	isolateRegistry(t)
	Register(Strategy{Name: "zeta"})
	Register(Strategy{Name: "alpha"})
	_, err := Lookup("nope")
	if err == nil {
		t.Fatal("Lookup of an unregistered kind returned no error")
	}
	want := `unknown RUNNER_KIND "nope"; registered runner families: [alpha zeta]`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestLookupEmptyKindSelectsOCI(t *testing.T) {
	isolateRegistry(t)
	Register(Strategy{Name: KindBwrap})
	Register(Strategy{Name: KindOCI})
	s, err := Lookup("")
	if err != nil {
		t.Fatalf("Lookup(\"\"): %v", err)
	}
	if s.Name != KindOCI {
		t.Errorf("Lookup(\"\").Name = %q, want %q", s.Name, KindOCI)
	}
}

func TestLookupReturnsRegisteredConstructors(t *testing.T) {
	isolateRegistry(t)
	built := ""
	Register(Strategy{
		Name:     "fake",
		New:      func(Config, string) Runner { built = "run"; return nil },
		NewBuild: func(Config, string) Runner { built = "build"; return nil },
	})
	s, err := Lookup("fake")
	if err != nil {
		t.Fatal(err)
	}
	s.New(Config{}, "")
	if built != "run" {
		t.Errorf("New dispatched to %q", built)
	}
	s.NewBuild(Config{}, "")
	if built != "build" {
		t.Errorf("NewBuild dispatched to %q", built)
	}
}

func TestNamesSorted(t *testing.T) {
	isolateRegistry(t)
	for _, n := range []string{"b", "c", "a"} {
		Register(Strategy{Name: n})
	}
	if got := Names(); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("Names() = %v", got)
	}
}
