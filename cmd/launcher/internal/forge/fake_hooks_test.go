package forge

import "testing"

func TestFake_HooksFireOnInstrumentedMethods(t *testing.T) {
	f := NewFake()
	fired := map[string]int{}
	for _, m := range []string{"Merge", "Rebase", "CheckState", "MarkReady", "ListPRFiles", "RelayBundle", "OpenPRForBranch", "TransitionState", "Comment", "CreateDraftPR"} {
		f.On(m, func(int) { fired[m]++ })
	}

	_ = f.Merge("u")
	_ = f.Rebase("u")
	_, _ = f.CheckState("u")
	_ = f.MarkReady("u")
	_, _ = f.ListPRFiles("u")
	_, _, _ = f.OpenPRForBranch("b")
	_ = f.TransitionState("1", Dispatchable, InProgress)
	_ = f.Comment("1", "c")
	_, _, _ = f.AsGithubReadOnly().(DraftPRCreator).CreateDraftPR("t", "b", "main", "h")
	_ = f.AsLocal().(BundleRelay).RelayBundle("/o", "ref")

	for m, n := range fired {
		if n != 1 {
			t.Errorf("%s hook fired %d times, want 1", m, n)
		}
	}
	if len(fired) != 10 {
		t.Errorf("hooks fired for %v, want all ten methods", fired)
	}
}
