package main

import (
	"fmt"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/branchrecovery"
)

// recoverBranch positions the agent branch ahead of everything that reads the
// tree: the registry's in-tree rewrite and the devShell probe both must see
// the rebased tree, and the rewrite must not dirty it before the rebase.
// Advise-only never cuts, adopts or rebases a branch (ADR 0022, issue #640)
// and a self-contained Box has no clone.
func (r *boxRun) recoverBranch() error {
	if r.env.SelfContained || r.adviseOnly {
		return nil
	}
	out, err := r.d.Recover(r.recoveryConfig(), r.openPRQuery(), r.d.Stdout)
	if err != nil {
		return err
	}
	r.recovery = out
	return nil
}

func (r *boxRun) recoveryConfig() branchrecovery.Config {
	return branchrecovery.Config{
		WorkDir:    r.in.WorkDir,
		Branch:     r.env.Branch,
		BaseBranch: r.env.BaseBranch,
		CodeForge:  r.codeForge(),
		Push:       r.env.BoxWriteEnabled,
		OutboxDir:  r.in.OutboxDir,
	}
}

// openPRQuery is the CODE_FORGE Strategy's in-box open-PR query bound to this
// Box's repo and branch; nil when the forge is unregistered or cannot be asked.
// A tokenless Box answers from the host's BOX_HOST_OPEN_PR instead (issue
// #7146). A failed query is an error, never "no PR": the caller must not
// force-reset on it.
func (r *boxRun) openPRQuery() func() (bool, error) {
	s, ok := backendkit.ByName(r.codeForge())
	if !ok || s.OpenPRForBranch == nil {
		return nil
	}
	if v := r.d.Getenv(branchrecovery.HostOpenPREnv); v != "" {
		return func() (bool, error) {
			switch v {
			case branchrecovery.HostOpenPROpen:
				return true, nil
			case branchrecovery.HostOpenPRNone:
				return false, nil
			}
			return false, fmt.Errorf("host could not answer whether a PR is open (BOX_HOST_OPEN_PR=%q)", v)
		}
	}
	return func() (bool, error) {
		return s.OpenPRForBranch(r.d.Getenv("REPO_SLUG"), r.env.Branch)
	}
}

// codeForge is CODE_FORGE, defaulting an unset one to github.
func (r *boxRun) codeForge() string {
	if r.env.CodeForge == "" {
		return backend.GitHub.Name
	}
	return r.env.CodeForge
}

// forgeDescriptor is codeForge's registry row. An unregistered name gets a zero
// Descriptor, so every in-box capability reads false.
func (r *boxRun) forgeDescriptor() backend.Descriptor {
	desc, _ := backend.ByName(r.codeForge())
	return desc
}
