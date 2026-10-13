package promptassembly

// accessForgeGates computes the box-access and code-forge gate family. It
// lives in its own file so edits to this axis do not collide with sibling axis
// tickets editing their own gate files (issue #2351, spec #2347 slice S3).
func accessForgeGates(e Env) map[string]bool {
	g := map[string]bool{}

	// Selected solely by BOX_WRITE_ENABLED, independent of ISSUE_TRACKER and
	// CODE_FORGE.
	g["BOX_ACCESS_READ_WRITE"] = e.BoxWriteEnabled
	g["BOX_ACCESS_READ_ONLY"] = !e.BoxWriteEnabled

	// nix resolves the backend suffix at eval time, so it arrives pre-resolved
	// on Env.ForgeBackend (issue #2533), but a host launcher older than #2533
	// never sets it. Re-derive it from e.CodeForge the way entrypoint.sh's
	// "${CODE_FORGE:-github}" did; hardcoding the GH arm would tell the agent
	// to drive `gh` against a Forgejo forge.
	backend := e.ForgeBackend
	if backend == "" {
		backend = "GH"
		if e.CodeForge == "forgejo" {
			backend = "FORGEJO"
		}
	}

	// The read-write fork for the OPEN A PULL REQUEST create step (issue
	// #5099). Read-only stays forge-agnostic, with no gate here at all.
	g["OPEN_PR_CREATE_RW_GH"] = backend == "GH" && e.BoxWriteEnabled
	g["OPEN_PR_CREATE_RW_FORGEJO"] = backend == "FORGEJO" && e.BoxWriteEnabled

	// The IF BLOCKED PR step and landing line name a PR, which only a forge
	// with PRs has (issue #5233). OutboxRelayCapable stands in for that: only
	// the github/forgejo forge rows set it, while git and local do not, and
	// git resolves to the GH backend so OPEN_PR_CREATE_RW_GH alone cannot
	// tell them apart.
	g["PR_FORGE_READ_WRITE"] = e.BoxWriteEnabled && e.OutboxRelayCapable
	g["NO_PR_FORGE_READ_WRITE"] = e.BoxWriteEnabled && !e.OutboxRelayCapable
	g["IF_BLOCKED_PR_RW_GH"] = g["OPEN_PR_CREATE_RW_GH"] && g["PR_FORGE_READ_WRITE"]
	g["IF_BLOCKED_PR_RW_FORGEJO"] = g["OPEN_PR_CREATE_RW_FORGEJO"] && g["PR_FORGE_READ_WRITE"]

	// On a host-mediated forge (ADR 0033) the clone source is a read-only
	// mount and the launcher lands the branch host-side, so a read-write Box
	// there never pushes (issue #5251); the no-PR gates above already cover
	// its PR step and landing line. Keyed on the bit, never on the CODE_FORGE
	// name.
	g["BOX_PUSH_READ_WRITE"] = e.BoxWriteEnabled && !e.HostMediatedRemote
	g["HOST_MEDIATED_READ_WRITE"] = e.BoxWriteEnabled && e.HostMediatedRemote

	// The fix-pass CI-read step forks on backend regardless of box access.
	g["FIX_CI_READ_GH"] = backend == "GH"
	g["FIX_CI_READ_FORGEJO"] = backend == "FORGEJO"

	return g
}
