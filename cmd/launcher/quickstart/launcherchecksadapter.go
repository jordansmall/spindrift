package main

import (
	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/launcherchecks"
)

// quickstartCheckConfig converts the wizard's answers into a
// launcherchecks.Config. Quickstart prompts for one backend and uses it as both
// the tracker and the forge, so answers has no codeForge field and the caller
// passes that name separately.
func quickstartCheckConfig(a answers, codeForge string) launcherchecks.Config {
	var ghToken string
	if harnessEnvTokenEnvVar(a.choice.name) == "GH_TOKEN" {
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

		IssueTracker: a.choice.name,
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
			// Each backend's own validators run against the wizard's answers; a
			// knob the wizard never collected reads as unset, so the row reports
			// it as `spindrift doctor` would. Only quickstart rows are ever the
			// scaffold's tracker or forge, so those are the rows probed.
			if s, ok := backendkit.ByName(name); ok {
				shared := quickstartShared(a.repoSlug, a.gitUserName, a.gitUserEmail)
				lookup := knobLookup(a.choice.knobs, s.TokenEnvVar, a.token)
				if s.ValidateTracker != nil {
					b.ValidateTracker = func() error { return s.ValidateTracker(shared, lookup) }
				}
				if s.ValidateCodeForge != nil {
					b.ValidateCodeForge = func() error { return s.ValidateCodeForge(shared, lookup) }
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
