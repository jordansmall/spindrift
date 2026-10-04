package main

import (
	"spindrift.dev/launcher/internal/branchrecovery"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/github"
)

// recoverBranch positions the agent branch ahead of everything that reads the
// tree: the guards, the registry's in-tree rewrite and the devShell probe all
// must see the rebased tree, and the rewrite must not dirty it before the
// rebase. Advise-only never cuts, adopts or rebases a branch (ADR 0022, issue
// #640) and a self-contained Box has no clone.
func (r *boxRun) recoverBranch() error {
	if r.env.SelfContained || r.adviseOnly {
		return nil
	}
	out, err := r.d.Recover(r.recoveryConfig(), r.openPR, r.d.Stdout)
	if err != nil {
		return err
	}
	r.recovery = out
	return nil
}

func (r *boxRun) recoveryConfig() branchrecovery.Config {
	codeForge := r.env.CodeForge
	if codeForge == "" {
		codeForge = "github"
	}
	return branchrecovery.Config{
		WorkDir:    r.in.WorkDir,
		Branch:     r.env.Branch,
		BaseBranch: r.env.BaseBranch,
		CodeForge:  codeForge,
		Push:       r.env.BoxWriteEnabled,
		OutboxDir:  r.in.OutboxDir,
	}
}

// openPR reports whether the agent branch has an open PR, through the github
// forge seam. A failed query is an error, never "no PR": the caller must not
// force-reset on it.
func (r *boxRun) openPR() (bool, error) {
	_, open, err := github.NewExecClient(r.d.Getenv("REPO_SLUG"), forge.DispatchLabels{}, "").OpenPRForBranch(r.env.Branch)
	return open, err
}
