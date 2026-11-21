package promptassembly

// accessForgeGates computes the code-forge gate family.
func accessForgeGates(e Env) map[string]bool {
	g := map[string]bool{}

	// A host launcher older than #2533 never sets ForgeBackend, so forgeSignal
	// re-derives it from CodeForge; hardcoding the GH arm would tell the agent
	// to drive `gh` against a Forgejo forge.
	backend := forgeSignal(e)

	// The IF BLOCKED PR step and landing line name a PR, which only a forge
	// with PRs has (issue #5233); OutboxRelayCapable stands in for that, set
	// only on the github/forgejo rows.
	g["PR_FORGE_READ_ONLY"] = e.OutboxRelayCapable
	// A Box on a no-PR forge has no PR step either (issue #6281). The name
	// carries no access infix, so lib/mkHarness.nix's
	// readOnlyReachableFragmentRows scans its fragments (if-blocked-pr-none.md,
	// if-blocked-outcome-landing-branch.md) for forbidden markers: keep them
	// free of `git push`/`gh pr` text.
	g["NO_PR_FORGE"] = !e.OutboxRelayCapable

	// On a host-mediated forge (ADR 0033) the clone source is a read-only
	// mount and the launcher lands the branch host-side; elsewhere the Box
	// writes its branch to the outbox as a bundle (issues #6259, #6281). The
	// COMMIT push step and the IF BLOCKED triage and push steps fork on host
	// mediation. Keyed on the bit, never on the CODE_FORGE name.
	g["HOST_MEDIATED_PUSH"] = e.HostMediatedRemote
	g["OUTBOX_PUSH_READ_ONLY"] = !e.HostMediatedRemote

	// The fix-pass CI-read step forks on backend.
	g["FIX_CI_READ_GH"] = backend == "GH"
	g["FIX_CI_READ_FORGEJO"] = backend == "FORGEJO"

	return g
}
