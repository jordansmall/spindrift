package main

import (
	"errors"
	"io"
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/branchrecovery"
	"spindrift.dev/launcher/internal/dispatchkind"
)

func TestRecoverBranch_RunsForAWorkDispatchWithTheEnvsConfig(t *testing.T) {
	f := newFixture(t)
	f.env.CodeForge = ""
	f.run()
	want := []branchrecovery.Config{{
		WorkDir: f.in.WorkDir, Branch: "agent/issue-42", BaseBranch: "main",
		CodeForge: "github", Push: false, OutboxDir: f.in.OutboxDir,
	}}
	if !reflect.DeepEqual(f.recoverCfgs, want) {
		t.Fatalf("recover configs = %+v, want %+v", f.recoverCfgs, want)
	}
}

func TestRecoverBranch_SkippedWithoutAClone(t *testing.T) {
	f := newFixture(t)
	f.env.SelfContained = true
	f.run()
	if len(f.recoverCfgs) != 0 {
		t.Errorf("recovery ran: %+v", f.recoverCfgs)
	}
}

func TestRecoverBranch_RunsExactlyForKindsThatLandCode(t *testing.T) {
	for _, d := range dispatchkind.All {
		t.Run(d.Name, func(t *testing.T) {
			f := newFixture(t)
			f.env.DispatchKind = d.Name
			f.run()
			if ran := len(f.recoverCfgs) == 1; ran == d.AdviseOnly {
				t.Errorf("AdviseOnly=%v but recovery calls = %d", d.AdviseOnly, len(f.recoverCfgs))
			}
		})
	}
}

func TestRecoverBranch_FailureIsAPhaseErrorBeforeToolchainAndAssembly(t *testing.T) {
	f := newFixture(t)
	f.recoverErr = errors.New("boom")
	rc, err := run(f.in, f.env, f.d)
	var pe *phaseError
	if !errors.As(err, &pe) || pe.phase != "branch-recovery" || rc != 0 {
		t.Fatalf("rc = %d, err = %v", rc, err)
	}
	if f.assembled != 0 || len(f.nixCalls) != 0 || len(f.prefetched) != 0 {
		t.Errorf("later phases ran: assembled=%d nix=%v prefetched=%d", f.assembled, f.nixCalls, len(f.prefetched))
	}
}

func TestRecoveryConfig_QueriesOpenPROnlyWhereTheBoxCan(t *testing.T) {
	for forge, want := range map[string]bool{
		"": true, "github": true,
		"forgejo": false, "git": false, "local": false, "bogus": false,
	} {
		t.Run("forge="+forge, func(t *testing.T) {
			f := newFixture(t)
			f.env.CodeForge = forge
			f.run()
			if len(f.recoverQueryable) != 1 {
				t.Fatalf("recover calls = %d, want one", len(f.recoverQueryable))
			}
			if got := f.recoverQueryable[0]; got != want {
				t.Errorf("open-PR query passed = %v, want %v", got, want)
			}
		})
	}
}

// queryFor runs the box with BOX_HOST_OPEN_PR=hostAnswer and returns the open-PR
// query it handed to recovery (nil when none was passed).
func queryFor(t *testing.T, forge, hostAnswer string) func() (bool, error) {
	t.Helper()
	// An empty PATH makes any gh invocation fail, so a host answer that wrongly
	// reached the forge CLI cannot pass as a clean "open" or "none".
	t.Setenv("PATH", t.TempDir())
	f := newFixture(t)
	f.env.CodeForge = forge
	f.knobs[branchrecovery.HostOpenPREnv] = hostAnswer
	var q func() (bool, error)
	rec := f.d.Recover
	f.d.Recover = func(cfg branchrecovery.Config, openPR func() (bool, error), w io.Writer) (branchrecovery.Outcome, error) {
		q = openPR
		return rec(cfg, openPR, w)
	}
	f.run()
	return q
}

func TestOpenPRQuery_AnswersFromTheHostVerdict(t *testing.T) {
	for answer, want := range map[string]bool{"open": true, "none": false} {
		t.Run(answer, func(t *testing.T) {
			q := queryFor(t, "github", answer)
			if q == nil {
				t.Fatal("no open-PR query passed")
			}
			if got, err := q(); err != nil || got != want {
				t.Errorf("query = (%v, %v), want (%v, nil)", got, err, want)
			}
		})
	}
}

func TestOpenPRQuery_AnUnusableHostVerdictIsAnError(t *testing.T) {
	for _, answer := range []string{"unknown", "garbage"} {
		t.Run(answer, func(t *testing.T) {
			q := queryFor(t, "github", answer)
			if q == nil {
				t.Fatal("no open-PR query passed")
			}
			if got, err := q(); err == nil || got {
				t.Errorf("query = (%v, %v), want an error and no PR", got, err)
			}
		})
	}
}

func TestOpenPRQuery_HostVerdictNeverAddsAQueryWhereNoneRan(t *testing.T) {
	if q := queryFor(t, "forgejo", "open"); q != nil {
		t.Error("forgejo Box was handed an open-PR query")
	}
}

func TestOpenPRQuery_UnsetHostVerdictKeepsTheForgeQuery(t *testing.T) {
	q := queryFor(t, "github", "")
	if q == nil {
		t.Fatal("no open-PR query passed")
	}
	if _, err := q(); err == nil {
		t.Error("query succeeded with no gh on PATH; the strategy was not consulted")
	}
}
