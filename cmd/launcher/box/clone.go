package main

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/boxclone"
)

// cloneTarget clones the Target repo into the work dir and stands in it, the
// way bash's clone_repo and `cd` did. A self-contained Box (issue #2202) has no
// repo to clone or explore, so it gets an empty work dir instead. It runs
// before branch recovery, which needs the clone, and after the kind check, so
// an unrecognized kind fails before anything is cloned.
func (r *boxRun) cloneTarget() error {
	if r.env.SelfContained {
		if err := os.MkdirAll(r.in.WorkDir, 0o755); err != nil {
			return phaseErr("clone", err)
		}
	} else if err := r.d.Clone(r.cloneConfig(), r.d.Stdout, r.d.Stderr); err != nil {
		return phaseErr("clone", err)
	}
	if err := r.d.Chdir(r.in.WorkDir); err != nil {
		return phaseErr("clone", fmt.Errorf("enter work dir: %w", err))
	}
	// Go's Chdir leaves PWD alone and the orchestrator inherits it, as it did
	// from bash's `cd`.
	if err := r.d.Setenv("PWD", r.in.WorkDir); err != nil {
		return phaseErr("env-export", err)
	}
	return nil
}

func (r *boxRun) cloneConfig() boxclone.Config {
	g := r.d.Getenv
	return boxclone.Config{
		CodeForge:          r.codeForge(),
		GHCredentialHelper: r.forgeDescriptor().InBoxGHCredentialHelper,
		HostMediatedRemote: r.env.HostMediatedRemote,
		RepoSlug:           g("REPO_SLUG"),
		Lookup:             envLookup(g),
		RepoMountDir:       r.in.RepoMountDir,
		WorkDir:            r.in.WorkDir,
		GitUserName:        g("GIT_USER_NAME"),
		GitUserEmail:       g("GIT_USER_EMAIL"),
	}
}

// envLookup serves a backend's knobs from the Box env; a blank value is unset,
// as a bare Getenv read has always treated it.
func envLookup(getenv func(string) string) backendkit.Lookup {
	return func(k string) (string, bool) {
		v := getenv(k)
		return v, v != ""
	}
}
