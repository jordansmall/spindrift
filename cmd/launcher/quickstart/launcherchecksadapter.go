package main

import (
	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/launcherchecks"
)

// quickstartCheckConfig converts the wizard's answers into a
// launcherchecks.Config. Quickstart prompts for one backend and uses it as both
// the tracker and the forge, so answers has no codeForge field and the caller
// passes that name separately.
func quickstartCheckConfig(a answers, codeForge string) launcherchecks.Config {
	var ghToken string
	if harnessEnvTokenEnvVar(a.tracker.issueTracker) == "GH_TOKEN" {
		ghToken = a.token
	}
	return launcherchecks.Config{
		RepoSlug:     a.repoSlug,
		GitUserName:  a.gitUserName,
		GitUserEmail: a.gitUserEmail,
		// The shared gh-token row reads GH_TOKEN, so the wizard's credential
		// belongs here only when its own harness.env writes it under that name.
		// Otherwise a scaffold that still needs GH_TOKEN (e.g. a jira tracker
		// on a github forge) would pass here and fail `spindrift doctor`.
		GHToken: ghToken,

		Driver:      a.driver,
		Credentials: credentialValues(a.credentials),

		Runtime: a.runtime,

		IssueTracker: a.tracker.issueTracker,
		CodeForge:    codeForge,

		// Models, ResearchDispatch, and SelfContained stay zero because the
		// wizard never prompts for them.
	}
}

// quickstartCheckDeps supplies launcherchecks' caller seams for Quickstart.
func quickstartCheckDeps(a answers) launcherchecks.Deps {
	return launcherchecks.Deps{
		// Quickstart runs before the CLI and has no input document to trust, so
		// the registry alone resolves the signals.
		Signals: launcherchecks.SignalsFromRegistry,
		Backend: func(name string) (launcherchecks.Backend, bool) {
			row, ok := backend.ByName(name)
			if !ok {
				return launcherchecks.Backend{}, false
			}
			b := launcherchecks.Backend{Descriptor: row}
			// forgejo is the only backend the wizard can validate: its validators
			// read FORGEJO_BASE_URL (derived from the git remote) and
			// FORGEJO_TOKEN (the credential the wizard acquires). Every other
			// backend declares no validator or reads a knob the wizard never
			// collects, so its row checks axis membership only.
			if name == backend.Forgejo.Name {
				b.ValidateTracker = func() error {
					return forgejo.ValidateForgejoEnv("ISSUE_TRACKER", a.tracker.forgejoBaseURL, a.token)
				}
				b.ValidateCodeForge = func() error {
					return forgejo.ValidateForgejoEnv("CODE_FORGE", a.tracker.forgejoBaseURL, a.token)
				}
			}
			return b, true
		},
		TrackerNames:   launcherchecks.TrackerNamesFromRegistry,
		CodeForgeNames: launcherchecks.CodeForgeNamesFromRegistry,

		// ExtraCrossKnob stays unset: Quickstart's scaffold has no
		// REGISTRY_PROXY_ROUTES_FILE knob.
	}
}

// credentialValues keys the gathered credentials by env name for
// launcherchecks.Config.Credentials; a blank placeholder stays unsatisfied.
func credentialValues(creds []credential) map[string]string {
	if len(creds) == 0 {
		return nil
	}
	m := make(map[string]string, len(creds))
	for _, c := range creds {
		m[c.env] = c.value
	}
	return m
}
