package forge_test

import (
	"fmt"
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

// TestResolveOpenPRFiles verifies ResolveOpenPRFiles absorbs the PRForge
// assertion and ListPRFiles call so callers don't need their own assertion
// after resolving: push-only and no-open-PR both yield (nil, nil), a found
// PR's changed files are returned, and a ListPRFiles failure propagates.
func TestResolveOpenPRFiles(t *testing.T) {
	t.Run("push-only forge has no PR to discover", func(t *testing.T) {
		f := forge.NewFake()
		f.BranchPrefix = "agent/issue-"

		got, err := forge.ResolveOpenPRFiles(f.AsPushOnly(), "42")
		if err != nil || got != nil {
			t.Fatalf("want nil, nil; got %v, err=%v", got, err)
		}
	})

	t.Run("no open PR yet", func(t *testing.T) {
		f := forge.NewFake()
		f.BranchPrefix = "agent/issue-"

		got, err := forge.ResolveOpenPRFiles(f, "42")
		if err != nil || got != nil {
			t.Fatalf("want nil, nil; got %v, err=%v", got, err)
		}
	})

	t.Run("open PR found returns its changed files", func(t *testing.T) {
		f := forge.NewFake()
		f.BranchPrefix = "agent/issue-"
		f.SetPR("agent/issue-42", forge.PR{URL: "https://github.com/o/r/pull/7"})
		f.SetPRFiles("https://github.com/o/r/pull/7", []string{"a.go", "b.go"})

		got, err := forge.ResolveOpenPRFiles(f, "42")
		if err != nil || len(got) != 2 || got[0] != "a.go" || got[1] != "b.go" {
			t.Fatalf("want [a.go b.go], nil; got %v, err=%v", got, err)
		}
	})

	t.Run("ListPRFiles failure propagates", func(t *testing.T) {
		f := forge.NewFake()
		f.BranchPrefix = "agent/issue-"
		f.SetPR("agent/issue-42", forge.PR{URL: "https://github.com/o/r/pull/7"})
		f.PRFilesErr = fmt.Errorf("boom")

		got, err := forge.ResolveOpenPRFiles(f, "42")
		if err == nil || got != nil {
			t.Fatalf("want nil, error; got %v, err=%v", got, err)
		}
	})
}
