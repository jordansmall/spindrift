package promptassembly

// accessForgeGates computes the box-access and code-forge gate family
// (entrypoint.sh: 940-989). It lives in its own file so edits to this axis do
// not collide with sibling axis tickets editing their own gate files (issue
// #2351, spec #2347 slice S3).
func accessForgeGates(e Env) map[string]bool {
	g := map[string]bool{}

	// Selected solely by BOX_WRITE_ENABLED, independent of ISSUE_TRACKER and
	// CODE_FORGE (entrypoint.sh: 940-957).
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

	// The create step's read-write fork (entrypoint.sh: 969-979). Read-only
	// stays forge-agnostic, with no gate here at all.
	g["OPEN_PR_CREATE_RW_GH"] = backend == "GH" && e.BoxWriteEnabled
	g["OPEN_PR_CREATE_RW_FORGEJO"] = backend == "FORGEJO" && e.BoxWriteEnabled

	// The fix-pass CI-read step forks on backend regardless of box access
	// (entrypoint.sh: 981-989).
	g["FIX_CI_READ_GH"] = backend == "GH"
	g["FIX_CI_READ_FORGEJO"] = backend == "FORGEJO"

	return g
}
