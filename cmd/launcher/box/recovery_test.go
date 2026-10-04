package main

import (
	"errors"
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/branchrecovery"
	"spindrift.dev/launcher/internal/dispatchkind"
)

func TestRecoverBranch_RunsForAWorkDispatchWithTheEnvsConfig(t *testing.T) {
	f := newFixture(t)
	f.env.CodeForge = ""
	f.env.BoxWriteEnabled = false
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
