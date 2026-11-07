package promptassembly

import (
	"slices"
	"testing"
)

// diffGateVocabulary reports each side of the Gates()/vocabulary seam that the
// other lacks. Names in exempt are vocabulary entries the caller does not
// expect computed to return, so they are never reported as unreturned.
func diffGateVocabulary(computed map[string]bool, vocabulary, exempt []string) (undeclared, unreturned []string) {
	for g := range computed {
		if !slices.Contains(vocabulary, g) {
			undeclared = append(undeclared, g)
		}
	}
	for _, g := range vocabulary {
		if _, ok := computed[g]; !ok && !slices.Contains(exempt, g) {
			unreturned = append(unreturned, g)
		}
	}
	slices.Sort(undeclared)
	slices.Sort(unreturned)
	return undeclared, unreturned
}

func TestDiffGateVocabulary(t *testing.T) {
	computed := map[string]bool{"A": true, "B": false}

	if u, r := diffGateVocabulary(computed, []string{"A", "B"}, nil); len(u)+len(r) != 0 {
		t.Errorf("equal sets reported undeclared=%v unreturned=%v", u, r)
	}
	if u, _ := diffGateVocabulary(computed, []string{"A"}, nil); !slices.Equal(u, []string{"B"}) {
		t.Errorf("undeclared = %v, want [B]", u)
	}
	if _, r := diffGateVocabulary(computed, []string{"A", "B", "C"}, nil); !slices.Equal(r, []string{"C"}) {
		t.Errorf("unreturned = %v, want [C]", r)
	}

	// An exempt name in the vocabulary is not reported, but it must not mask a
	// missing non-exempt one.
	if u, r := diffGateVocabulary(computed, []string{"A", "B", "X"}, []string{"X"}); len(u)+len(r) != 0 {
		t.Errorf("exempt name reported undeclared=%v unreturned=%v", u, r)
	}
	if _, r := diffGateVocabulary(computed, []string{"A", "B", "C", "X"}, []string{"X"}); !slices.Equal(r, []string{"C"}) {
		t.Errorf("unreturned = %v, want [C]", r)
	}
}
