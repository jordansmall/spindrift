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
// is re-resolved. It keeps a 60-minute App token fresh only when the command
// mints one per call: one read from a refresher file (rewritten every 45
// minutes) can already be 45 minutes old.
const connectorCredentialRefresh = 30 * time.Minute

// secretCmdRunner runs a credential's `_CMD` form with no stdin, so a vault
// prompt cannot hang the Daemon. It is the test seam.
var secretCmdRunner = func(cmd string) (string, error) {
	return secretcmd.Run(cmd, false)
}

// resolveConnectorCredentials fills the credential env of each Connector row
// the Daemon reads through, from `_CMD` or the SECRET_CMD template as the
// Launcher does. refresh re-resolves them, keeping the old value on failure;
// it is nil when none were sourced.
//
// `_CMD` wins over a plain value and a template value is pinned as `_CMD`
// too, or a restart-in-place successor would inherit a frozen Setenv'd token.
// The pin makes Launcher children run `_CMD` without their
// connectorCredentialNeededThisRun check, harmless while they share the
// Daemon's ISSUE_TRACKER and CODE_FORGE.
// The CODE_FORGE row is included, though the probe reads only the tracker:
// children inherit its pinned `_CMD`, and refusing here fails fast. A row
// counts when backendkit.{Forge,Tracker}ReadsThroughConnector holds. A
// Basic-scheme row's IdentityEnv has no `_CMD` form and is
// not resolved.
func resolveConnectorCredentials(doc *inputdoc.Document, kinds []daemon.Kind, stderr io.Writer) (refresh func(), err error) {
	probes := false
	for _, k := range kinds {
		d, ok := dispatchkind.ByVerb(string(k))
		if !ok {
			continue
		}
		// Exhaustive, so a new DemandSource cannot be skipped silently.
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
		readsTracker := row.Name == tracker && backendkit.TrackerReadsThroughConnector(row.Name)
		readsForge := row.Name == forge && backendkit.ForgeReadsThroughConnector(row.Name)
		if !(readsTracker || readsForge) || seen[env] {
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
