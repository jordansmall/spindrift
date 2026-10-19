package main

import (
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/launcherchecks"
)

// launcherCheckConfig adapts config to launcherchecks.Config. It narrows
// dispatchKind to the ResearchDispatch bool that launcherchecks'
// repo-slug/gh-token exemption needs (true when the kind's Tracker axis names
// the research tracker), since launcherchecks has no dispatch kind string.
func launcherCheckConfig(c config) launcherchecks.Config {
	researchDispatch := false
	switch c.kind().Tracker {
	case dispatchkind.TrackerResearch:
		researchDispatch = true
	}
	return launcherchecks.Config{
		RepoSlug:     c.repoSlug,
		GitUserName:  c.gitUserName,
		GitUserEmail: c.gitUserEmail,
		GHToken:      c.ghToken,

		Driver: c.driver,
		// The per-tier models are read from getenvSchema, as the --secret-cmd
		// fallback reads them, so the probe and the fallback agree on which
		// tiers can select a credential-gated Provider.
		Models: modelTiers(func(env string) string {
			if env == "MODEL" {
				return c.model
			}
			return getenvSchema(env)
		}),
		Credentials: launcherCredentials(c),

		IssueTracker: c.issueTracker,
		CodeForge:    c.codeForge,

		ResearchDispatch: researchDispatch,
		SelfContained:    c.selfContained,
	}
}

// launcherCredentials is the credential env name -> value map launcherchecks
// evaluates Driver credential rules against. Every env var a Driver's rules
// name needs an entry (TestLauncherCredentialsCoverDriverRules).
func launcherCredentials(c config) map[string]string {
	return map[string]string{
		"CLAUDE_CODE_OAUTH_TOKEN": c.claudeOAuthToken,
		"ANTHROPIC_API_KEY":       c.anthropicAPIKey,
		"OPENCODE_AUTH_CONTENT":   c.opencodeAuthContent,
		"PI_AUTH_CONTENT":         c.piAuthContent,
		"OPENAI_API_KEY":          c.openaiAPIKey,
		"GEMINI_API_KEY":          c.geminiAPIKey,
		"OPENROUTER_API_KEY":      c.openrouterAPIKey,
		"GROQ_API_KEY":            c.groqAPIKey,
		"XAI_API_KEY":             c.xaiAPIKey,
		"MISTRAL_API_KEY":         c.mistralAPIKey,
		"DEEPSEEK_API_KEY":        c.deepseekAPIKey,
	}
}

// launcherCheckDeps supplies launcherchecks' caller seams. A nil
// validateTracker or validateCodeForge on a backend row must stay nil here:
// crossKnobCheck's "no extra validation" arm keys off that nilness.
func launcherCheckDeps(c config) launcherchecks.Deps {
	return launcherchecks.Deps{
		Signals: func(codeForge, issueTracker string) launcherchecks.Signals {
			sig := resolveCapabilitySignals(codeForge, issueTracker)
			return launcherchecks.Signals{
				InBoxUnreachableTracker: sig.inBoxUnreachableTracker,
				FullyLocal:              sig.fullyLocal,
			}
		},
		Backend: func(name string) (launcherchecks.Backend, bool) {
			row, ok := backendByName(name)
			if !ok {
				return launcherchecks.Backend{}, false
			}
			b := launcherchecks.Backend{Descriptor: row.Descriptor}
			if row.validateTracker != nil {
				b.ValidateTracker = func() error { return row.validateTracker(c) }
			}
			if row.validateCodeForge != nil {
				b.ValidateCodeForge = func() error { return row.validateCodeForge(c) }
			}
			return b, true
		},
		TrackerNames:   validTrackerNames,
		CodeForgeNames: validCodeForgeNames,
	}
}

// launcherCrossKnobDeps is launcherCheckDeps plus the one extra cross-knob
// row cmd/launcher has and Quickstart doesn't. It is a separate builder so
// the required-knob path, which never reads ExtraCrossKnob, doesn't build a
// row it then discards.
func launcherCrossKnobDeps(c config) launcherchecks.Deps {
	d := launcherCheckDeps(c)
	d.ExtraCrossKnob = []doctor.Check{registryProxyRoutesCheck(c, true)}
	return d
}
