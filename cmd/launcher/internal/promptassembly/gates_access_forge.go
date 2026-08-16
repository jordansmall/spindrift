package promptassembly

// accessForgeGates computes the box-access and code-forge gate family
// (entrypoint.sh: 940-989) -- kept in its own file, disjoint from gates.go's
// other axes, so this axis's future edits never collide with sibling axis
// tickets (issue #2351, spec #2347 slice S3) editing their own gate files.
func accessForgeGates(e Env) map[string]bool {
	g := map[string]bool{}

	// The OPEN A PULL REQUEST push step gate (entrypoint.sh: 940-957):
	// selected solely by BOX_WRITE_ENABLED, independent of ISSUE_TRACKER/
	// CODE_FORGE (not nested under the write-step gates above).
	g["BOX_ACCESS_READ_WRITE"] = e.BoxWriteEnabled
	g["BOX_ACCESS_READ_ONLY"] = !e.BoxWriteEnabled

	// CODE_FORGE -> backend suffix (entrypoint.sh: 959-967): only forgejo
	// diverges from the shared gh-flavored path (github/git/local all ride
	// the GH arm), matching "${CODE_FORGE:-github}". nix already resolves
	// this switch once, at eval time, so the backend suffix arrives
	// pre-resolved on Env.ForgeBackend rather than being re-derived here
	// from CodeForge (issue #2533).
	backend := e.ForgeBackend

	// The OPEN A PULL REQUEST create step's read-write fork (entrypoint.sh:
	// 969-979): only fires on the resolved backend, and only when
	// BOX_ACCESS_READ_WRITE is on -- read-only stays forge-agnostic (no gate
	// here at all).
	g["OPEN_PR_CREATE_RW_GH"] = backend == "GH" && e.BoxWriteEnabled
	g["OPEN_PR_CREATE_RW_FORGEJO"] = backend == "FORGEJO" && e.BoxWriteEnabled

	// The fix-pass CONTEXT CI-read step's backend fork (entrypoint.sh:
	// 981-989): fires unconditionally on the resolved backend, regardless of
	// box access.
	g["FIX_CI_READ_GH"] = backend == "GH"
	g["FIX_CI_READ_FORGEJO"] = backend == "FORGEJO"

	return g
}
