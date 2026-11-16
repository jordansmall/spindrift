package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/secretcmd"
)

// connectorCredentialRefresh is how often a `_CMD`-sourced Connector credential
// is re-resolved. A GitHub App installation token lives 60 minutes, so this
// keeps it fresh only when the command mints a new token per call: one reading
// a cached token (a refresher file rewritten every 45 minutes) can already be
// 45 minutes old.
const connectorCredentialRefresh = 30 * time.Minute

// secretCmdRunner runs a credential's `_CMD` form with no stdin, so a vault
// prompt cannot hang the Daemon. It is the test seam.
var secretCmdRunner = func(cmd string) (string, error) {
	return secretcmd.Run(cmd, false)
}

// resolveConnectorCredentials makes the credential env of each Connector row
// the Daemon reads through hold a value, from `_CMD` or the SECRET_CMD
// template as the Launcher does. refresh re-resolves those once, keeping the
// old value on failure; it is nil when none were sourced.
//
// `_CMD` wins over a plain value, and a template-sourced value is pinned as
// `_CMD` too: a restart-in-place successor inherits the Setenv'd value, so a
// plain-wins rule would freeze a stale token there. Basic-scheme rows'
// IdentityEnv has no `_CMD` form and is not resolved. That pin exports the
// command to every Launcher child, which runs an explicit `_CMD` without its
// needed-this-run check (connectorCredentialNeededThisRun); harmless while
// children share the Daemon's ISSUE_TRACKER and CODE_FORGE. A primary row of
// the CODE_FORGE is included, though the Daemon's own probe reads only the
// tracker: its children inherit the pinned `_CMD`, and refusing here fails fast.
// Only a primary row counts as the forge (not backendkit.ForgeReadsThroughConnector),
// so a plain routed row that is merely the default CODE_FORGE stays unresolved;
// a routed non-primary row counts only as the tracker, as before.
func resolveConnectorCredentials(doc *inputdoc.Document, kinds []daemon.Kind, stderr io.Writer) (refresh func(), err error) {
	probes := false
	for _, k := range kinds {
		d, ok := dispatchkind.ByVerb(string(k))
		if !ok {
			continue
		}
		// Exhaustive like buildDemandSources, so a new DemandSource cannot be
		// skipped silently here.
		switch d.DemandSource {
		case dispatchkind.DemandTrackerProbe:
			probes = true
		case dispatchkind.DemandHostOutbox, dispatchkind.DemandChildReported:
		default:
			panic(fmt.Sprintf("daemon: kind %s has unhandled DemandSource %d", k, d.DemandSource))
		}
	}
	if !probes {
		return nil, nil
	}
	type cmdCredential struct{ env, cmd string }
	var sourced []cmdCredential
	tracker, forge := issueTrackerName(doc), codeForgeName(doc)
	tmpl := os.Getenv("SECRET_CMD")
	seen := map[string]bool{}
	for _, row := range backendrows.All() {
		env := row.CredentialEnv
		readsTracker := row.Name == tracker && backendkit.TrackerReadsThroughConnector(row.Name, row.Primary)
		// Only a primary forge row: a plain routed row that is merely the default
		// CODE_FORGE must never be newly resolved.
		readsForge := row.Name == forge && row.Primary
		if !(readsTracker || readsForge) || row.Shadow || seen[env] {
			continue
		}
		seen[env] = true
		cmd := secretcmd.Command(env, tmpl)
		if cmd == "" {
			if secretcmd.IsSet(env) {
				continue
			}
			msg := fmt.Sprintf("the %s Connector row is read through its Connector this run but its credential is unset: set %s, %s_CMD, or SECRET_CMD", row.Name, env, env)
			if row.CredentialKnob != "" {
				msg += fmt.Sprintf(" (knob %s)", row.CredentialKnob)
			}
			return nil, errors.New(msg)
		}
		if err := secretcmd.Export(env, cmd, secretCmdRunner); err != nil {
			return nil, fmt.Errorf("the %s Connector row's credential comes from %s_CMD or the SECRET_CMD template: %w", row.Name, env, err)
		}
		if err := secretcmd.Pin(env, cmd); err != nil {
			return nil, err
		}
		sourced = append(sourced, cmdCredential{env, cmd})
	}
	if len(sourced) == 0 {
		return nil, nil
	}
	return func() {
		for _, c := range sourced {
			if err := secretcmd.Export(c.env, c.cmd, secretCmdRunner); err != nil {
				fmt.Fprintf(stderr, "daemon: connector credential %s refresh failed, keeping the previous value: %v\n", c.env, err)
			}
		}
	}, nil
}

// startConnectorCredentialWatch is the test seam for mainRun's watcher start.
var startConnectorCredentialWatch = func(refresh func(), interval time.Duration) {
	go watchConnectorCredentials(refresh, interval)
}

// watchConnectorCredentials calls refresh every interval, forever.
func watchConnectorCredentials(refresh func(), interval time.Duration) {
	for range time.Tick(interval) {
		refresh()
	}
}
