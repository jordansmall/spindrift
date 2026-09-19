package promptassembly

import (
	"reflect"
	"testing"
)

// pairEnvMatrix builds its Env set by reflection rather than from a hand-listed
// set, so a pair keyed off a newly added Env bool is swept as soon as the field
// exists and neither end of the pair mechanic needs a matching edit here.
func pairEnvMatrix() []Env {
	typ := reflect.TypeOf(Env{})
	allTrue := reflect.New(typ).Elem()

	envs := []Env{{}}
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Type.Kind() != reflect.Bool {
			continue
		}
		one := reflect.New(typ).Elem()
		one.Field(i).SetBool(true)
		envs = append(envs, one.Interface().(Env))
		allTrue.Field(i).SetBool(true)
	}
	envs = append(envs, allTrue.Interface().(Env))

	kinds := []string{"", defaultDispatchKind, "research"}
	out := make([]Env, 0, len(envs)*len(kinds))
	for _, e := range envs {
		for _, k := range kinds {
			e.DispatchKind = k
			out = append(out, e)
		}
	}
	return out
}

// TestRegistryInverseOfPairsAreExactlyOneOn is the Go half of the exactly-one-on
// pair mechanic lib/fragment-pairs.nix validates: nix checks that a pair is
// declared well-formedly, but Gates computes the two gates independently, so only
// the real computation proves a pair never renders both members or neither. It
// reads the registry, so a new inverseOf row in lib/fragments.nix is covered here.
func TestRegistryInverseOfPairsAreExactlyOneOn(t *testing.T) {
	reg, err := LoadRegistryFile("testdata/registry.json")
	if err != nil {
		t.Fatalf("LoadRegistryFile: %v", err)
	}

	envs := pairEnvMatrix()
	declared := 0
	for _, row := range reg.Rows {
		if row.InverseOf == "" {
			continue
		}
		declared++

		var sawOn, sawOff bool
		for _, env := range envs {
			g := Gates(env)
			on, ok := g[row.Gate]
			if !ok {
				t.Fatalf("Gates has no gate %q (declared inverseOf %q)", row.Gate, row.InverseOf)
			}
			inverse, ok := g[row.InverseOf]
			if !ok {
				t.Fatalf("Gates has no gate %q (named as %q's inverseOf)", row.InverseOf, row.Gate)
			}
			if on == inverse {
				t.Fatalf("Gates(%+v): %q = %v and its inverseOf %q = %v, want exactly one on", env, row.Gate, on, row.InverseOf, inverse)
			}
			if on {
				sawOn = true
			} else {
				sawOff = true
			}
		}

		// Both arms have to be observed, or the complement assertion above
		// passes vacuously on a matrix that never moves the knob behind the pair.
		if !sawOn || !sawOff {
			t.Errorf("gate %q (inverseOf %q) was %v for every Env in the matrix; the matrix never exercised the knob behind the pair", row.Gate, row.InverseOf, sawOn)
		}
	}

	if declared == 0 {
		t.Fatal("no registry row carries inverseOf; either lib/fragments.nix stopped declaring pairs or FragmentRow stopped decoding the column")
	}
}
