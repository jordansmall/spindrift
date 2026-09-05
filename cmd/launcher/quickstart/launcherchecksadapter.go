package main

import (
	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/launcherchecks"
)

// quickstartCheckConfig adapts a (plus the selected backend's name,
// codeForge — answers has no codeForge field of its own since Quickstart
// uses one prompted backend for both the tracker and the forge) to
// launcherchecks.Config.
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
		// credential belongs here only when its own harness.env writes it
		// under that name — otherwise `spindrift doctor` would contradict
		// the scaffold the wizard just wrote.
		GHToken: ghToken,

		ClaudeOAuthToken: a.claudeOAuthToken,
		AnthropicAPIKey:  a.anthropicAPIKey,

		Runtime: a.runtime,

		IssueTracker: a.tracker.issueTracker,
		CodeForge:    codeForge,

		// Driver, Model, OpencodeAuthContent, ResearchDispatch, and
		// SelfContained stay at their zero values — the wizard never
		// prompts for them. A zero Driver lands on the driver-credentials
		// row's claude arm, the credential the wizard did prompt for above.
	}
}

// quickstartCheckDeps supplies launcherchecks' caller seams for Quickstart,
// closing over a for the per-backend validators that have a Quickstart
// equivalent to validate.
func quickstartCheckDeps(a answers) launcherchecks.Deps {
	return launcherchecks.Deps{
		// Quickstart is pre-CLI and has no input document to trust, so the
		// registry-only resolution is the whole story here.
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
			// forgejo is the only backend the wizard can validate: its
			// validators read knobs (FORGEJO_BASE_URL, FORGEJO_TOKEN) the
			// wizard actually prompts for. github declares no validator at
			// all, and every other backend's reads a knob the wizard never
			// collects, so those stay nil and their rows check axis
			// membership only.
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
