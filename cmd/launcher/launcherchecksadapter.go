package main

import (
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/launcherchecks"
)

// launcherCheckConfig adapts config to launcherchecks.Config, field for
// field. dispatchKind/selfContained narrow to the two bools the shared
// package's repo-slug/gh-token exemption needs; the shared package has no
// concept of a dispatch kind string.
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

// launcherCheckDeps supplies launcherchecks' caller seams:
// resolveCapabilitySignals (the loadedDoc-trusting resolver main.go keeps
// for itself, narrowed to the two fields the exemption reads), backendRows
// via backendByName (each row's own validateTracker/validateCodeForge bound
// to this c as zero-arg closures — a nil validator on the row must stay nil
// here, since crossKnobCheck's "no extra validation" arm keys off that
// nilness), and the two valid-name lists.
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

// launcherCrossKnobDeps is launcherCheckDeps plus registryProxyRoutesCheck
// as the one extra cross-knob row cmd/launcher has and Quickstart doesn't.
// It hangs off its own builder rather than off launcherCheckDeps so the
// required-knob path, which never reads ExtraCrossKnob, doesn't build a row
// it then discards.
func launcherCrossKnobDeps(c config) launcherchecks.Deps {
	d := launcherCheckDeps(c)
	d.ExtraCrossKnob = []doctor.Check{registryProxyRoutesCheck(c, true)}
	return d
}
