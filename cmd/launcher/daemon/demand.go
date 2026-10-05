package main

import (
	"os"
	"time"

	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/inputdoc"
)

// demandSources maps each kind the daemon schedules from tracker demand
// (ADR 0059) to the counter that answers its Demand probe. A kind absent
// from the map stays exit-driven.
type demandSources map[daemon.Kind]forge.DemandCounter

// buildDemandSources builds a demand counter for each kind in kinds whose
// descriptor row says DemandTrackerProbe, from the same settings a child
// resolves ISSUE_TRACKER, LOCAL_ISSUES_DIR and the work labels from. A
// tracker without a forge.DemandCounter adapter (everything but local so
// far) or a missing knob yields no source, never an error: the kind then
// simply stays exit-driven.
//
// A relative LOCAL_ISSUES_DIR is used as written: it resolves against the
// daemon's cwd, which every child inherits (RunChild sets no cmd.Dir), so
// both sides read the same directory.
func buildDemandSources(doc *inputdoc.Document, kinds []daemon.Kind) demandSources {
	var probed []*dispatchkind.Descriptor
	for _, k := range kinds {
		if d, ok := dispatchkind.ByVerb(string(k)); ok && d.DemandSource == dispatchkind.DemandTrackerProbe {
			probed = append(probed, d)
		}
	}
	if len(probed) == 0 {
		return nil
	}

	if childKnob(doc, "ISSUE_TRACKER", os.Getenv("ISSUE_TRACKER")) != "local" {
		return nil
	}
	dir := childKnob(doc, "LOCAL_ISSUES_DIR", os.Getenv("LOCAL_ISSUES_DIR"))
	if dir == "" {
		return nil
	}
	configured := forge.DispatchLabels{
		Dispatchable: childKnob(doc, "LABEL", os.Getenv("LABEL")),
		InProgress:   childKnob(doc, "IN_PROGRESS_LABEL", os.Getenv("IN_PROGRESS_LABEL")),
		Complete:     childKnob(doc, "COMPLETE_LABEL", os.Getenv("COMPLETE_LABEL")),
		Failed:       childKnob(doc, "FAILED_LABEL", os.Getenv("FAILED_LABEL")),
	}

	sources := demandSources{}
	for _, d := range probed {
		labels := forge.FamilyLabels(d.Labels, configured)
		if labels.Dispatchable == "" {
			continue
		}
		counter, ok := forge.IssueTracker(local.NewLocalTracker(dir, labels)).(forge.DemandCounter)
		if !ok {
			continue
		}
		sources[daemon.KindOf(d)] = counter
	}
	return sources
}

// parseProbeInterval turns DAEMON_PROBE_INTERVAL's resolved value into the
// override of every tracker's default probe interval; blank is 0, meaning
// keep the per-tracker defaults.
func parseProbeInterval(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	return inputdoc.ParseDuration("DAEMON_PROBE_INTERVAL", "duration of at least 1s", raw, time.Second)
}

// probeIntervals is daemon.Config.ProbeIntervals for src: each source's own
// interval, or override when it is non-zero. Only kinds with a source get an
// entry, so an override never turns an exit-driven kind into a probed one.
func probeIntervals(src demandSources, override time.Duration) map[daemon.Kind]time.Duration {
	out := make(map[daemon.Kind]time.Duration, len(src))
	for k, c := range src {
		interval := c.ProbeInterval()
		if override > 0 {
			interval = override
		}
		out[k] = interval
	}
	return out
}

// childKnob is the value a child launcher resolves for key. Every key the
// --input document carries is stripped from the child's env (withoutKeys), so
// the child reads the document alone and an ambient override is invisible to
// it; a key the document lacks is not stripped, so the child inherits the
// daemon's ambient value. The caller passes ambient as a literal os.Getenv so
// knob_env_guard_test.go still sees which keys the daemon reads.
func childKnob(doc *inputdoc.Document, key, ambient string) string {
	if doc != nil {
		if v, ok := doc.Settings[key]; ok {
			return v
		}
	}
	return ambient
}
