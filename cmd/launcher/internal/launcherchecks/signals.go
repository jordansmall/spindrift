package launcherchecks

import "spindrift.dev/launcher/internal/backend"

// SignalsFromRegistry resolves Signals for a CodeForge/IssueTracker pairing from
// the backend registry alone. Quickstart runs before the CLI loads a config
// document, so it cannot use cmd/launcher's resolveCapabilitySignals, which also
// trusts a nix-forwarded artifact when the loaded document's pairing matches.
func SignalsFromRegistry(codeForge, issueTracker string) Signals {
	codeForgeRow, _ := backend.ByName(codeForge)
	trackerRow, _ := backend.ByName(issueTracker)
	return Signals{
		InBoxUnreachableTracker: trackerRow.InBoxUnreachableTracker,
		FullyLocal:              codeForgeRow.HostMediatedRemote && trackerRow.InBoxUnreachableTracker,
	}
}
