package main

import (
	"fmt"
	"os"
	"time"

	"spindrift.dev/launcher/internal/connectorsetup"
	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/inputdoc"
)

// connectorCredentialRefresh is how often a `_CMD`-sourced Connector credential
// is re-resolved. It keeps a 60-minute App token fresh only when the command
// mints one per call: one read from a refresher file (rewritten every 45
// minutes) can already be 45 minutes old.
const connectorCredentialRefresh = 30 * time.Minute

// newConnectorRuntime is how the Daemon builds its Connector runtime. A var so
// a test binary, which has no runner to start a sandbox on, installs the
// in-process engine instead.
var newConnectorRuntime = connectorsetup.New

// connectorInput says which credential rows the Daemon reads. Only a kind that
// probes the tracker reads any, resolving them through their `_CMD` and
// refusing an unset credential: otherwise no row is read this run, and each
// keeps a plain-value source.
//
// Values come from the document, then the process environment, as a Launcher
// child reads them. The CODE_FORGE row is read though the probe reads only the
// tracker, so that refusing fails fast.
func connectorInput(doc *inputdoc.Document, kinds []daemon.Kind, pwd string) connectorsetup.Input {
	shared := doc.Artifacts.Shared
	in := connectorsetup.Input{
		Dir: pwd, RunnerKind: shared.RunnerKind.String(), Runtime: shared.Runtime.String(),
		Artifacts: doc.Artifacts, Knobs: childLookup(doc),
		SecretCmd: os.Getenv("SECRET_CMD"),
		Setting:   func(env string) string { v, _ := childEnvValue(doc, env); return v },
	}
	for _, k := range kinds {
		d, ok := dispatchkind.ByVerb(string(k))
		if !ok {
			continue
		}
		// Exhaustive, so a new DemandSource cannot be skipped silently.
		switch d.DemandSource {
		case dispatchkind.DemandTrackerProbe:
			in.Forge, in.Tracker = codeForgeName(doc), issueTrackerName(doc)
		case dispatchkind.DemandHostOutbox, dispatchkind.DemandChildReported:
		default:
			panic(fmt.Sprintf("daemon: kind %s has unhandled DemandSource %d", k, d.DemandSource))
		}
	}
	return in
}
