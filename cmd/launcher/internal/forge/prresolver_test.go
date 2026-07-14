package forge_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestResolveOpenPR verifies forge.ResolveOpenPR's single documented
// draft/absent policy: a push-only Code Forge (no PRForge) and "no open PR
// yet" both resolve to Found: false with no error; a found PR reports its
// URL and draft flag, leaving callers to decide what to do with IsDraft.
func TestResolveOpenPR(t *testing.T) {
	t.Run("push-only forge has no PR to discover", func(t *testing.T) {
		f := forge.NewFake()
		f.BranchPrefix = "agent/issue-"
		f.SetPR("agent/issue-42", forge.PR{URL: "https://github.com/o/r/pull/7"})

		res, err := forge.ResolveOpenPR(f.AsPushOnly(), "42")
		if err != nil || res.Found {
			t.Fatalf("want {Found:false} nil; got %+v, err=%v", res, err)
		}
	})

	t.Run("no open PR yet", func(t *testing.T) {
		f := forge.NewFake()
		f.BranchPrefix = "agent/issue-"

		res, err := forge.ResolveOpenPR(f, "42")
		if err != nil || res.Found {
			t.Fatalf("want {Found:false} nil; got %+v, err=%v", res, err)
		}
	})

	t.Run("open PR found reports URL and draft flag", func(t *testing.T) {
		f := forge.NewFake()
		f.BranchPrefix = "agent/issue-"
		f.SetPR("agent/issue-42", forge.PR{URL: "https://github.com/o/r/pull/7", IsDraft: true})

		res, err := forge.ResolveOpenPR(f, "42")
		if err != nil || !res.Found || res.URL != "https://github.com/o/r/pull/7" || !res.IsDraft {
			t.Fatalf("want {Found:true URL:.../pull/7 IsDraft:true} nil; got %+v, err=%v", res, err)
		}
	})
}
