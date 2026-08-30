package main

import "fmt"

// validateGHAppConfig enforces GH_APP_ID, GH_APP_PRIVATE_KEY_FILE, and
// GH_APP_INSTALLATION_ID (issue #2867, local dispatch auth via a GitHub App
// installation token) as an all-or-nothing trio: minting a token needs every
// one of the three, so exactly one or two set is a misconfiguration rather
// than a partially working feature. Zero set is the pre-issue-#2867
// default — local dispatch auth is opt-in — and all three set is the only
// other valid state.
//
// It also rejects a fully configured trio combined with an explicit
// GH_TOKEN_REFRESH_FILE: once all three are set, bootstrap's own minting
// loop (ghapptoken.Watch, see bootstrap.go's applyGHAppToken) owns and
// rewrites its own token file, so an operator-supplied GH_TOKEN_REFRESH_FILE
// at the same time is ambiguous about which mechanism owns the file — this
// fails closed rather than silently letting one mechanism win. A partial
// trio combined with GH_TOKEN_REFRESH_FILE surfaces the partial-config error
// instead, the more actionable diagnosis of the two.
//
// Used both as bootstrap.go's pre-mint gate (called against the raw,
// not-yet-mutated config, before applyGHAppToken ever sets
// c.ghTokenRefreshFile itself) and as the "gh-app-config" row in
// launcherCrossKnobChecks (checks.go), so `spindrift doctor` reports the
// same diagnosis validate(c)/bootstrap() would.
func validateGHAppConfig(appID, privateKeyFile, installationID, tokenRefreshFile string) error {
	var missing []string
	if appID == "" {
		missing = append(missing, "GH_APP_ID")
	}
	if privateKeyFile == "" {
		missing = append(missing, "GH_APP_PRIVATE_KEY_FILE")
	}
	if installationID == "" {
		missing = append(missing, "GH_APP_INSTALLATION_ID")
	}

	switch len(missing) {
	case 3:
		// None set: opt-out default, always fine.
		return nil
	case 0:
		// All set: fine, unless it collides with an explicit
		// GH_TOKEN_REFRESH_FILE.
		if tokenRefreshFile != "" {
			return fmt.Errorf("GH_APP_ID, GH_APP_PRIVATE_KEY_FILE, and GH_APP_INSTALLATION_ID are mutually exclusive with GH_TOKEN_REFRESH_FILE: local dispatch's GitHub App minting owns and rewrites its own token file, so an explicitly-set GH_TOKEN_REFRESH_FILE is ambiguous about which mechanism owns it")
		}
		return nil
	default:
		return fmt.Errorf("GH_APP_ID, GH_APP_PRIVATE_KEY_FILE, and GH_APP_INSTALLATION_ID must be set together or not at all; missing: %s", joinOxford(missing))
	}
}

// ghAppConfigured reports whether c carries the full GH_APP_ID/
// GH_APP_PRIVATE_KEY_FILE/GH_APP_INSTALLATION_ID trio (issue #2867). Used by
// checks.go's "gh-token" required-knob row to exempt a local-dispatch
// operator who authenticates purely via the App trio from the GH_TOKEN
// presence check: bootstrap's own applyGHAppToken (bootstrap.go) mints
// c.ghToken from that trio before validate(c) ever runs, but `spindrift
// doctor`'s validateConfig (main.go) never mints — it only validates the
// loaded config as-is, deliberately without a network call (see
// validateConfig's own doc comment) — so it must recognize the trio as
// sufficient on its own rather than require a token that will only exist
// once bootstrap actually mints one. A partial trio still fails here (this
// only checks presence), but launcherCrossKnobChecks' own "gh-app-config"
// row, which runs validateGHAppConfig, already reports that specific
// misconfiguration with a clearer message.
func ghAppConfigured(c config) bool {
	return c.ghAppID != "" && c.ghAppPrivateKeyFile != "" && c.ghAppInstallationID != ""
}
