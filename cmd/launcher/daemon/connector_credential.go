package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"spindrift.dev/launcher/internal/backend/backendrows"
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

// resolveConnectorCredentials fills the credential env of each Connector row
// the Daemon reads through (see rowcredential for precedence). refresh
// re-resolves them, keeping the old value on failure; it is nil when none were
// sourced, and only a kind that probes the tracker resolves at all.
//
// A template value is pinned as `_CMD` too, or a restart-in-place successor
// would inherit a frozen Setenv'd token. A Launcher child's parseFlags runs a
// pinned core secret's `_CMD` whatever row it reads, harmless while it shares
// the Daemon's ISSUE_TRACKER and CODE_FORGE. The CODE_FORGE row is resolved
// though the probe reads only the tracker: children inherit its pinned `_CMD`,
// and refusing here fails fast.
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
	res := rowcredential.Resolution{
		Forge: codeForgeName(doc), Tracker: issueTrackerName(doc),
		Template: os.Getenv("SECRET_CMD"), Run: secretCmdRunner,
	}
	recipes, err := res.Apply(backendrows.All())
	if err != nil {
		return nil, err
	}
	for _, r := range recipes {
		if err := secretcmd.Pin(r.Env, r.Cmd); err != nil {
			return nil, err
		}
	}
	if len(recipes) == 0 {
		return nil, nil
	}
	return func() {
		res.Refresh(recipes, func(env string, err error) {
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
