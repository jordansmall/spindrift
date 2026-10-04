package main

// missingEnvError names the first required Box env var that was unset or empty.
type missingEnvError struct{ Name, Msg string }

func (e *missingEnvError) Error() string { return e.Msg }

// checkEnvGuards is the env preamble entrypoint.sh ran under `set -e`, in its
// order. An empty value counts as missing, like bash's `${X:?}`.
func checkEnvGuards(getenv func(string) string) error {
	set := func(name string) bool { return getenv(name) != "" }
	// The launcher's validate() made the fully-local call and forwards it as
	// BOX_FULLY_LOCAL (issue #2527). Self-contained research (issue #2202)
	// clones no repo; SELF_CONTAINED stays a raw per-dispatch input, and the
	// local-tracker half arrives as BOX_IN_BOX_UNREACHABLE_TRACKER.
	needsForge := !set("BOX_FULLY_LOCAL") &&
		!(getenv("SELF_CONTAINED") == "1" && set("BOX_IN_BOX_UNREACHABLE_TRACKER"))
	require := func(name, msg string) error {
		if set(name) {
			return nil
		}
		return &missingEnvError{Name: name, Msg: msg}
	}
	if needsForge {
		if err := require("GH_TOKEN", "GH_TOKEN is required"); err != nil {
			return err
		}
	}
	// The kind axes come from dispatch.buildBoxEnv (ADR 0056, issue #3996);
	// box never branches on DISPATCH_KIND for them.
	for _, name := range []string{"DISPATCH_KEY", "DISPATCH_KEYING", "DISPATCH_ANNOUNCE_VERB"} {
		if err := require(name, name+" is required"); err != nil {
			return err
		}
	}
	// A chore-keyed Dispatch (the butler) carries one Ledger Chore, never a
	// tracker issue, so it requires CHORE_NAME in ISSUE_NUMBER's place.
	keyed := "ISSUE_NUMBER"
	if getenv("DISPATCH_KEYING") == "chore" {
		keyed = "CHORE_NAME"
	}
	if err := require(keyed, keyed+" is required"); err != nil {
		return err
	}
	if needsForge {
		if err := require("REPO_SLUG", "REPO_SLUG (owner/repo) is required"); err != nil {
			return err
		}
	}
	for _, name := range []string{"GIT_USER_NAME", "GIT_USER_EMAIL"} {
		if err := require(name, name+" is required"); err != nil {
			return err
		}
	}
	return nil
}
