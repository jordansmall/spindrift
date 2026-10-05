package main

import (
	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/branchrecovery"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/github"
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
	out, err := r.d.Recover(r.recoveryConfig(), r.openPR, r.d.Stdout)
	if err != nil {
		return err
	}
	r.recovery = out
	return nil
}

func (r *boxRun) recoveryConfig() branchrecovery.Config {
	return branchrecovery.Config{
		WorkDir:     r.in.WorkDir,
		Branch:      r.env.Branch,
		BaseBranch:  r.env.BaseBranch,
		CodeForge:   r.codeForge(),
		QueryOpenPR: r.forgeDescriptor().InBoxOpenPRQueryable,
		Push:        r.env.BoxWriteEnabled,
		OutboxDir:   r.in.OutboxDir,
	}
}

// openPR reports whether the agent branch has an open PR, through the github
// forge seam. A failed query is an error, never "no PR": the caller must not
// force-reset on it.
func (r *boxRun) openPR() (bool, error) {
	_, open, err := github.NewExecClient(r.d.Getenv("REPO_SLUG"), forge.DispatchLabels{}, "").OpenPRForBranch(r.env.Branch)
	return open, err
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
