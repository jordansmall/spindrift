package main

import (
	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
)

// configureBackendCLIs runs ConfigureCLI for each distinct backend the Box's
// ISSUE_TRACKER and CODE_FORGE select; an unset axis defaults to github, as
// the rest of the Box reads it. An unregistered name has no setup to run.
func (r *boxRun) configureBackendCLIs() error {
	cli := backendkit.BoxCLI{
		Getenv:   r.d.Getenv,
		LookPath: r.d.LookPath,
		Run:      r.d.RunCmd,
		Stderr:   r.d.Stderr,
	}
	tracker := r.issueTracker()
	names := []string{tracker}
	if forge := r.codeForge(); forge != tracker {
		names = append(names, forge)
	}
	for _, name := range names {
		s, ok := backendkit.ByName(name)
		if !ok || s.ConfigureCLI == nil {
			continue
		}
		if err := s.ConfigureCLI(cli); err != nil {
			return err
		}
	}
	return nil
}

// issueTracker is ISSUE_TRACKER, defaulting an unset one to github.
func (r *boxRun) issueTracker() string {
	if r.env.IssueTracker == "" {
		return backend.GitHub.Name
	}
	return r.env.IssueTracker
}
