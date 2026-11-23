package main

import (
	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/launcherchecks"
)

// quickstartCheckConfig converts the wizard's answers into a
// launcherchecks.Config.
func quickstartCheckConfig(a answers) launcherchecks.Config {
	var ghToken string
	for _, c := range a.chosen() {
		if harnessEnvTokenEnvVar(c.name) == "GH_TOKEN" {
			ghToken = a.tokens[c.name]
		}
	}
	return launcherchecks.Config{
		RepoSlug:     a.repoSlug,
		GitUserName:  a.gitUserName,
		GitUserEmail: a.gitUserEmail,
		// Set only when harness.env writes a credential as GH_TOKEN, so a
		// scaffold still needing GH_TOKEN fails here as it would under
		// `spindrift doctor`.
		GHToken: ghToken,

		Driver:      a.driver,
		Credentials: credentialValues(a.credentials),

		IssueTracker: a.tracker.name,
		CodeForge:    a.forge.name,

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
			// Validators run against the wizard's answers; an uncollected knob
			// reads as unset, as under `spindrift doctor`. Only registered rows
			// can be the scaffold's tracker or forge, so only they are probed.
			if s, ok := backendkit.ByName(name); ok {
				shared := quickstartShared(a.repoSlug, a.gitUserName, a.gitUserEmail)
				lookup := a.lookup()
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
