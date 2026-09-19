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
		// The shared gh-token row is GH_TOKEN-specific, so the wizard's
		// credential belongs here only when its own harness.env writes it under
		// that name. Otherwise `spindrift doctor` contradicts the scaffold the
		// wizard just wrote.
		GHToken: ghToken,

		ClaudeOAuthToken: a.claudeOAuthToken,
		AnthropicAPIKey:  a.anthropicAPIKey,

		Runtime: a.runtime,

		IssueTracker: a.tracker.issueTracker,
		CodeForge:    codeForge,

		// Driver, Model, OpencodeAuthContent, ResearchDispatch, and
		// SelfContained stay zero because the wizard never prompts for them. A
		// zero Driver lands on the driver-credentials row's claude arm, the
		// credential the wizard did prompt for above.
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
			b := launcherchecks.Backend{
				ValidAsTracker:   row.ValidAsTracker,
				ValidAsCodeForge: row.ValidAsCodeForge,
			}
			// forgejo is the only backend the wizard can validate, because its
			// validators read knobs (FORGEJO_BASE_URL, FORGEJO_TOKEN) the wizard
			// prompts for. Every other backend declares no validator or reads a
			// knob the wizard never collects, so its row checks axis membership
			// only.
			if name == backend.Forgejo.Name {
				validateForgejo := func() error {
					return forgejo.ValidateForgejoEnv(a.tracker.forgejoBaseURL, a.token)
				}
				b.ValidateTracker = validateForgejo
				b.ValidateCodeForge = validateForgejo
			}
			return b, true
		},
		TrackerNames:   launcherchecks.TrackerNamesFromRegistry,
		CodeForgeNames: launcherchecks.CodeForgeNamesFromRegistry,

		// ExtraCrossKnob stays unset: Quickstart's scaffold has no
		// REGISTRY_PROXY_ROUTES_FILE knob.
	}
}
