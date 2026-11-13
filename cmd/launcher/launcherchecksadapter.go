package main

import (
	"spindrift.dev/launcher/internal/backendkit"
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
		RepoSlug:     c.RepoSlug,
		GitUserName:  c.GitUserName,
		GitUserEmail: c.GitUserEmail,
		GHToken:      c.GhToken,

		Driver: c.driver,
		// The per-tier models are read from resolvedKnob, as the --secret-cmd
		// fallback reads them, so the probe and the fallback agree on which
		// tiers can select a credential-gated Provider.
		Models: modelTiers(func(env string) string {
			if env == "MODEL" {
				return c.Model
			}
			return resolvedKnob(env)
		}),
		Credentials: launcherCredentials(c),

		IssueTracker: c.IssueTracker,
		CodeForge:    c.CodeForge,

		ResearchDispatch: researchDispatch,
		SelfContained:    c.selfContained,
		BoxReadOnly:      c.effectiveBoxForgeAndIssueAccess() == "read-only",
	}
}

// launcherCredentials is the credential env name -> value map launcherchecks
// evaluates Driver credential rules against. Every env var a Driver's rules
// name needs an entry (TestLauncherCredentialsCoverDriverRules).
func launcherCredentials(c config) map[string]string {
	return map[string]string{
		"CLAUDE_CODE_OAUTH_TOKEN": c.ClaudeOAuthToken,
		"ANTHROPIC_API_KEY":       c.AnthropicAPIKey,
		"OPENCODE_AUTH_CONTENT":   c.OpencodeAuthContent,
		"PI_AUTH_CONTENT":         c.PiAuthContent,
		"OPENAI_API_KEY":          c.OpenaiAPIKey,
		"GEMINI_API_KEY":          c.GeminiAPIKey,
		"OPENROUTER_API_KEY":      c.OpenrouterAPIKey,
		"GROQ_API_KEY":            c.GroqAPIKey,
		"XAI_API_KEY":             c.XaiAPIKey,
		"MISTRAL_API_KEY":         c.MistralAPIKey,
		"DEEPSEEK_API_KEY":        c.DeepseekAPIKey,
	}
}

// launcherCheckDeps supplies launcherchecks' caller seams. A nil
// ValidateTracker or ValidateCodeForge on a backend row must stay nil here:
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
			row, ok := backendkit.ByName(name)
			if !ok {
				return launcherchecks.Backend{}, false
			}
			b := launcherchecks.Backend{Descriptor: row.Descriptor}
			if row.ValidateTracker != nil {
				b.ValidateTracker = func() error { return row.ValidateTracker(backendShared(c), c.knobLookup) }
			}
			if row.ValidateCodeForge != nil {
				b.ValidateCodeForge = func() error { return row.ValidateCodeForge(backendShared(c), c.knobLookup) }
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
