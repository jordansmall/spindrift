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
		OutboxDir:  r.in.OutboxDir,
	}
}

// openPRQuery is the open-PR question this Box asks, answered from the host's
// BOX_HOST_OPEN_PR (issue #7146); nil when the forge is unregistered or the
// host does not answer for it. The Box holds no forge client or token, so an
// unset or unusable answer is an error, never "no PR": the caller must not
// force-reset on it.
func (r *boxRun) openPRQuery() func() (bool, error) {
	if s, ok := backendkit.ByName(r.codeForge()); !ok || !s.HostAnswersOpenPR {
		return nil
	}
	v := r.d.Getenv(branchrecovery.HostOpenPREnv)
	return func() (bool, error) {
		switch v {
		case branchrecovery.HostOpenPROpen:
			return true, nil
		case branchrecovery.HostOpenPRNone:
			return false, nil
		}
		return false, fmt.Errorf("host could not answer whether a PR is open (%s=%q)", branchrecovery.HostOpenPREnv, v)
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
