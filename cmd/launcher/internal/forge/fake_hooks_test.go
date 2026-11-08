package forge

import "testing"

func TestFake_HooksFireOnInstrumentedMethods(t *testing.T) {
	f := NewFake()
	fired := map[string]int{}
	for _, m := range []string{"Merge", "Rebase", "CheckState", "MarkReady", "ListPRFiles", "RelayBundle"} {
		f.On(m, func(int) { fired[m]++ })
	}

	_ = f.Merge("u")
	_ = f.Rebase("u")
	_, _ = f.CheckState("u")
	_ = f.MarkReady("u")
	_, _ = f.ListPRFiles("u")
	_ = f.AsLocal().(BundleRelay).RelayBundle("/o", "ref")

	for m, n := range fired {
		if n != 1 {
			t.Errorf("%s hook fired %d times, want 1", m, n)
		}
	}
	if len(fired) != 6 {
		t.Errorf("hooks fired for %v, want all six methods", fired)
	}
}
