package promptassembly

import (
	"reflect"
	"testing"
)

// pairEnvMatrix enumerates Env values that toggle every bool field
// independently (the zero Env, one Env per bool field set alone, and an
// all-bools-true Env), each crossed with the dispatch kinds Gates
// distinguishes. Built by reflection rather than a hand-listed set so a
// future pair keyed off a newly added Env bool is swept the moment the
// field exists, matching the registry-driven shape of the test below --
// neither end of the pair mechanic should need a matching edit here.
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

// TestRegistryInverseOfPairsAreExactlyOneOn is the Go half of the
// exactly-one-on pair mechanic lib/fragment-pairs.nix validates: nix checks
// that a pair is *declared* well-formedly, but the two gates are computed
// independently in Gates, so only exercising the real computation proves a
// declared pair never renders both members or neither. Registry-driven on
// purpose -- every row that grows an `inverseOf` is covered the moment it is
// declared in lib/fragments.nix, with no matching edit here.
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

		// Without this the pair would pass vacuously on a matrix that never
		// moves the knob behind it -- both arms have to be observed for the
		// complement assertion above to have tested anything.
		if !sawOn || !sawOff {
			t.Errorf("gate %q (inverseOf %q) was %v for every Env in the matrix; the matrix never exercised the knob behind the pair", row.Gate, row.InverseOf, sawOn)
		}
	}

	if declared == 0 {
		t.Fatal("no registry row carries inverseOf; either lib/fragments.nix stopped declaring pairs or FragmentRow stopped decoding the column")
	}
}
