package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/credsource"
	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/rowcredential"
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

// resolveConnectorCredentials chooses the credential source of every Connector
// row for the runtime (see rowcredential for precedence); nothing is exported
// to the process environment. refresh re-runs the `_CMD` sources, keeping the
// old value on failure; it is nil when none exist. Only a kind that probes the
// tracker resolves rows through their `_CMD`, or refuses an unset credential:
// otherwise no row is read this run, and each keeps a plain-value source.
//
// Values come from the document, then the process environment, as a Launcher
// child reads them. The CODE_FORGE row is resolved though the probe reads only
// the tracker, so that refusing here fails fast.
func resolveConnectorCredentials(doc *inputdoc.Document, kinds []daemon.Kind, stderr io.Writer) (creds map[string]credsource.Row, refresh func(), err error) {
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
	res := rowcredential.Resolution{
		Template: os.Getenv("SECRET_CMD"), Run: secretCmdRunner,
		Lookup: func(env string) string { v, _ := childEnvValue(doc, env); return v },
	}
	if probes {
		res.Forge, res.Tracker = codeForgeName(doc), issueTrackerName(doc)
	}
	sourced, err := res.Sources(backendrows.All())
	if err != nil {
		return nil, nil, err
	}
	if len(sourced.Commands) == 0 {
		return sourced.Rows, nil, nil
	}
	return sourced.Rows, func() {
		sourced.Refresh(func(env string, err error) {
			fmt.Fprintf(stderr, "daemon: connector credential %s refresh failed, keeping the previous value: %v\n", env, err)
		})
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
