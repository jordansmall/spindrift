package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/trackerbuild"
)

// jiraProbeTimeout bounds each Jira Demand probe: the adapter otherwise falls
// back to the untimed http.DefaultClient, and a server that accepts the
// connection but never answers would stall the kind's probe for good.
const jiraProbeTimeout = 30 * time.Second

// demandSources maps each kind the daemon schedules from tracker demand
// (ADR 0059) to the counter that answers its Demand probe. A kind absent
// from the map stays exit-driven.
type demandSources map[daemon.Kind]forge.DemandCounter

// missingSources maps each probed kind that got no Demand source to the reason
// it stays exit-driven.
type missingSources map[daemon.Kind]string

// buildDemandSources builds a demand counter for each kind in kinds whose
// descriptor row says DemandTrackerProbe, from the same settings a child
// resolves ISSUE_TRACKER, the tracker's own knobs and the work labels from; a
// blank ISSUE_TRACKER is github, as the launcher defaults it. An unknown
// tracker or a missing knob yields no source, never an error: the kind then
// stays exit-driven. The second result names, per probed kind left without a
// source, why (never a token or secret value); warnUnprobedKinds reports it.
//
// FORGEJO_TOKEN and JIRA_TOKEN are read as plain knobs; a token supplied only
// through its -file or -cmd form is invisible here, so that deployment stays
// exit-driven; a set -cmd form earns a hint in the reason (tokenFormHint).
//
// A relative LOCAL_ISSUES_DIR is used as written: it resolves against the
// daemon's cwd, which every child inherits (RunChild sets no cmd.Dir), so
// both sides read the same directory.
//
// The github probe runs `gh` in the daemon's own environment, so it sees the
// daemon's GH_TOKEN; mainRun keeps that fresh from GH_TOKEN_REFRESH_FILE the
// way a child does.
func buildDemandSources(doc *inputdoc.Document, kinds []daemon.Kind) (demandSources, missingSources) {
	var probed []*dispatchkind.Descriptor
	for _, k := range kinds {
		if d, ok := dispatchkind.ByVerb(string(k)); ok && d.DemandSource == dispatchkind.DemandTrackerProbe {
			probed = append(probed, d)
		}
	}
	if len(probed) == 0 {
		return nil, nil
	}

	// Every knob is read whatever the tracker, so one Settings feeds whichever
	// adapter ISSUE_TRACKER names. The branch prefix is left blank: it only names
	// agent branches, which counting never touches.
	s := trackerbuild.Settings{
		RepoSlug:          childKnob(doc, "REPO_SLUG", os.Getenv("REPO_SLUG")),
		LocalIssuesDir:    childKnob(doc, "LOCAL_ISSUES_DIR", os.Getenv("LOCAL_ISSUES_DIR")),
		ForgejoBaseURL:    childKnob(doc, "FORGEJO_BASE_URL", os.Getenv("FORGEJO_BASE_URL")),
		ForgejoToken:      childKnob(doc, "FORGEJO_TOKEN", os.Getenv("FORGEJO_TOKEN")),
		JiraBaseURL:       childKnob(doc, "JIRA_BASE_URL", os.Getenv("JIRA_BASE_URL")),
		JiraProjectKey:    childKnob(doc, "JIRA_PROJECT_KEY", os.Getenv("JIRA_PROJECT_KEY")),
		JiraEmail:         childKnob(doc, "JIRA_EMAIL", os.Getenv("JIRA_EMAIL")),
		JiraToken:         childKnob(doc, "JIRA_TOKEN", os.Getenv("JIRA_TOKEN")),
		JiraStatusMapping: childKnob(doc, "JIRA_STATUS_MAPPING", os.Getenv("JIRA_STATUS_MAPPING")),
		JiraHTTPClient:    &http.Client{Timeout: jiraProbeTimeout},
	}
	name := issueTrackerName(doc)
	tr, ok := trackerbuild.ByName(name)
	if !ok {
		return nil, allMissing(probed, fmt.Sprintf("unknown ISSUE_TRACKER %q", name))
	}
	if err := tr.Validate(s); err != nil {
		return nil, allMissing(probed, err.Error()+tokenFormHint(doc, name, s))
	}
	configured := forge.DispatchLabels{
		Dispatchable: childKnob(doc, "LABEL", os.Getenv("LABEL")),
		InProgress:   childKnob(doc, "IN_PROGRESS_LABEL", os.Getenv("IN_PROGRESS_LABEL")),
		Complete:     childKnob(doc, "COMPLETE_LABEL", os.Getenv("COMPLETE_LABEL")),
		Failed:       childKnob(doc, "FAILED_LABEL", os.Getenv("FAILED_LABEL")),
	}

	missing := missingSources{}
	sources := demandSources{}
	for _, d := range probed {
		labels := forge.FamilyLabels(d.Labels, configured)
		if labels.Dispatchable == "" {
			missing[daemon.KindOf(d)] = "set LABEL (the dispatchable label is blank)"
			continue
		}
		s.Labels = labels
		// Only the tracker's capabilities matter: no code forge is in play, and the
		// descriptors feed fields counting never reads.
		counter := forge.ResolveCapabilities(nil, tr.New(s), backend.Descriptor{}, backend.Descriptor{}).DemandCounter
		if counter == nil {
			missing[daemon.KindOf(d)] = fmt.Sprintf("ISSUE_TRACKER=%s has no Demand adapter", name)
			continue
		}
		sources[daemon.KindOf(d)] = counter
	}
	return sources, missing
}

// allMissing is the missing result when no probed kind can have a source.
func allMissing(probed []*dispatchkind.Descriptor, reason string) missingSources {
	missing := make(missingSources, len(probed))
	for _, d := range probed {
		missing[daemon.KindOf(d)] = reason
	}
	return missing
}

// tokenFormHint explains a blank plain token when the tracker's _CMD form is
// set: the operator believes the token is supplied, but the daemon never runs
// the command. It names the knobs only, never the command.
func tokenFormHint(doc *inputdoc.Document, tracker string, s trackerbuild.Settings) string {
	var plain, cmd string
	switch {
	case tracker == "forgejo" && s.ForgejoToken == "":
		plain, cmd = "FORGEJO_TOKEN", childKnob(doc, "FORGEJO_TOKEN_CMD", os.Getenv("FORGEJO_TOKEN_CMD"))
	case tracker == "jira" && s.JiraToken == "":
		plain, cmd = "JIRA_TOKEN", childKnob(doc, "JIRA_TOKEN_CMD", os.Getenv("JIRA_TOKEN_CMD"))
	}
	if cmd == "" {
		return ""
	}
	return fmt.Sprintf("; the daemon reads %s only, not %s_CMD", plain, plain)
}

// warnUnprobedKinds says once at startup, on stderr and in the event stream,
// that each kind in missing (buildDemandSources' second result) is scheduled
// from child exits instead of tracker demand: a silent fallback would leave
// the operator believing the probe was running.
func warnUnprobedKinds(missing missingSources, stderr io.Writer, em *daemon.Emitter) {
	kinds := make([]daemon.Kind, 0, len(missing))
	for k := range missing {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	for _, k := range kinds {
		fmt.Fprintf(stderr, "daemon: kind %s has no Demand source: %s; scheduling it from child exits instead\n", k, missing[k])
		em.Emit(daemon.Event{Event: "demand_source_missing", Kind: k, Reason: missing[k]})
	}
}

// issueTrackerName is the ISSUE_TRACKER a child resolves.
func issueTrackerName(doc *inputdoc.Document) string {
	return childKnob(doc, "ISSUE_TRACKER", os.Getenv("ISSUE_TRACKER"))
}

// trackers is daemon.Config.Trackers for src: every probed kind counts against
// the one ISSUE_TRACKER, so they share a rate-limit pause (ADR 0059).
func trackers(src demandSources, doc *inputdoc.Document) map[daemon.Kind]string {
	name := issueTrackerName(doc)
	out := make(map[daemon.Kind]string, len(src))
	for k := range src {
		out[k] = name
	}
	return out
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

// childKnob is the value a child launcher resolves for key. A key whose
// --input document value counts (Document.Setting) is stripped from the
// child's env (withoutKeys), so the child reads the document alone and an
// ambient override is invisible to it; any other key is not stripped, so the
// child inherits the daemon's ambient value. The caller passes ambient as a
// literal os.Getenv so knob_env_guard_test.go still sees which keys the daemon
// reads. A key unset in both the document and the environment falls back to
// its schema default, as the child's schemaDefault does.
func childKnob(doc *inputdoc.Document, key, ambient string) string {
	if v, ok := childValue(doc, key, ambient); ok {
		return v
	}
	return inputdoc.SchemaDefault(key)
}

// requiredChildKnob is childKnob for a knob the daemon needs a value for: one
// absent from both the document and the environment is a configuration error
// (as Document.Resolve reports it), not a schema default.
func requiredChildKnob(doc *inputdoc.Document, key, ambient string) (string, error) {
	if v, ok := childValue(doc, key, ambient); ok {
		return v, nil
	}
	return "", inputdoc.MissingValueError(key)
}

// childValue is the document-then-ambient part of the child's resolution.
func childValue(doc *inputdoc.Document, key, ambient string) (string, bool) {
	if v, ok := doc.Setting(key); ok {
		return v, true
	}
	return ambient, ambient != ""
}

// childSharedKnobs are the raw values of the knobs the daemon resolves as a
// child does (issue #4623): the document first, then the ambient env.
type childSharedKnobs struct {
	baseBranch, maxParallel, awakeWindow          string
	butlerChores, butlerEvery, butlerChoreClasses string
}

// resolveChildSharedKnobs resolves every knob both the daemon and its
// children read. BASE_BRANCH and MAX_PARALLEL are required; the rest fall
// back to their schema default, so an empty BUTLER_CHORES is the default, not
// a configuration error.
func resolveChildSharedKnobs(doc *inputdoc.Document) (childSharedKnobs, error) {
	var k childSharedKnobs
	var err error
	if k.baseBranch, err = requiredChildKnob(doc, "BASE_BRANCH", os.Getenv("BASE_BRANCH")); err != nil {
		return k, err
	}
	if k.maxParallel, err = requiredChildKnob(doc, "MAX_PARALLEL", os.Getenv("MAX_PARALLEL")); err != nil {
		return k, err
	}
	k.awakeWindow = childKnob(doc, "DAEMON_AWAKE_WINDOW", os.Getenv("DAEMON_AWAKE_WINDOW"))
	k.butlerChores = childKnob(doc, "BUTLER_CHORES", os.Getenv("BUTLER_CHORES"))
	k.butlerEvery = childKnob(doc, "BUTLER_EVERY", os.Getenv("BUTLER_EVERY"))
	k.butlerChoreClasses = childKnob(doc, "BUTLER_CHORE_CLASSES", os.Getenv("BUTLER_CHORE_CLASSES"))
	return k, nil
}

// daemonOnlyRaw are the raw values of the daemonOnlyKnobs. selfApp and
// researchReservation stay empty when their gate is off.
type daemonOnlyRaw struct {
	app, selfApp, idleFloor, idleCap, probeInterval string
	failureBackoff, breakerThreshold, breakerWindow string
	researchReservation                             string
}

// resolveDaemonOnlyKnobs resolves, through Document.Lookup, the knobs no child
// reads, so a non-empty ambient value still wins (ADR 0020).
// DAEMON_SELF_APP is read only when the self-change check is on (withSelf),
// RESEARCH_RESERVATION only for a multi-kind daemon (multiKind).
func resolveDaemonOnlyKnobs(doc *inputdoc.Document, stderr io.Writer, withSelf, multiKind bool) (daemonOnlyRaw, error) {
	var k daemonOnlyRaw
	var err error
	if k.app, err = doc.Resolve("DAEMON_APP", stderr); err != nil {
		return k, err
	}
	if k.idleFloor, err = doc.Resolve("DAEMON_IDLE_FLOOR", stderr); err != nil {
		return k, err
	}
	if k.idleCap, err = doc.Resolve("DAEMON_IDLE_CAP", stderr); err != nil {
		return k, err
	}
	k.probeInterval = doc.ResolveOptional("DAEMON_PROBE_INTERVAL", stderr)
	if k.failureBackoff, err = doc.Resolve("DAEMON_FAILURE_BACKOFF", stderr); err != nil {
		return k, err
	}
	if k.breakerThreshold, err = doc.Resolve("DAEMON_BREAKER_THRESHOLD", stderr); err != nil {
		return k, err
	}
	if k.breakerWindow, err = doc.Resolve("DAEMON_BREAKER_WINDOW", stderr); err != nil {
		return k, err
	}
	if withSelf {
		if k.selfApp, err = doc.Resolve("DAEMON_SELF_APP", stderr); err != nil {
			return k, err
		}
	}
	if multiKind {
		if k.researchReservation, err = doc.Resolve("RESEARCH_RESERVATION", stderr); err != nil {
			return k, err
		}
	}
	return k, nil
}
