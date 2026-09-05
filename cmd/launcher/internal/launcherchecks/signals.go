package launcherchecks

import "spindrift.dev/launcher/internal/backend"

// SignalsFromRegistry resolves Signals for a CodeForge/IssueTracker pairing
// straight from the backend registry, with no config-loaded-document trust
// branch: cmd/launcher's own resolveCapabilitySignals additionally trusts a
// nix-forwarded artifact when the loaded document's pairing still matches
// the resolved one (main.go), a loadedDoc concept Quickstart has no
// equivalent of at its pre-CLI stage. Quickstart's Deps.Signals is this
// function; cmd/launcher keeps its own loadedDoc-aware resolver and adapts
// it to the Deps.Signals shape instead.
func SignalsFromRegistry(codeForge, issueTracker string) Signals {
	codeForgeRow, _ := backend.ByName(codeForge)
	trackerRow, _ := backend.ByName(issueTracker)
	return Signals{
		InBoxUnreachableTracker: trackerRow.InBoxUnreachableTracker,
		FullyLocal:              codeForgeRow.HostMediatedRemote && trackerRow.InBoxUnreachableTracker,
	}
}
