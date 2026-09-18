package main

import (
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/launcherchecks"
)

// launcherCheckConfig adapts config to launcherchecks.Config. It narrows
// dispatchKind to the ResearchDispatch bool that launcherchecks'
// repo-slug/gh-token exemption needs, since launcherchecks has no dispatch
// kind string.
func launcherCheckConfig(c config) launcherchecks.Config {
	return launcherchecks.Config{
		RepoSlug:     c.repoSlug,
		GitUserName:  c.gitUserName,
		GitUserEmail: c.gitUserEmail,
		GHToken:      c.ghToken,

		Driver:              c.driver,
		Model:               c.model,
		ClaudeOAuthToken:    c.claudeOAuthToken,
		AnthropicAPIKey:     c.anthropicAPIKey,
		OpencodeAuthContent: c.opencodeAuthContent,

		Runtime: c.runtime,

		IssueTracker: c.issueTracker,
		CodeForge:    c.codeForge,

		ResearchDispatch: c.dispatchKind == dispatchKindResearch,
		SelfContained:    c.selfContained,
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
			b := launcherchecks.Backend{
				ValidAsTracker:   row.ValidAsTracker,
				ValidAsCodeForge: row.ValidAsCodeForge,
			}
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
