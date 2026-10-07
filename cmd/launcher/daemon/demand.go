package main

import (
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strconv"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/recoverrecord"
	"spindrift.dev/launcher/internal/trackerbuild"
)

// jiraProbeTimeout bounds each Jira Demand probe: the adapter otherwise falls
// back to the untimed http.DefaultClient, and a server that accepts the
// connection but never answers would stall the kind's probe for good.
const jiraProbeTimeout = 30 * time.Second

// demandSources maps each kind the daemon schedules from host-side demand
// (ADR 0059: a tracker probe or the outbox count) to the counter that answers
// its Demand probe. A kind absent from the map stays exit-driven.
type demandSources map[daemon.Kind]forge.DemandCounter

// missingSources maps each probed kind that got no Demand source to the reason
// it stays exit-driven.
type missingSources map[daemon.Kind]string

// buildDemandSources builds the Demand counter of each kind in kinds the
// daemon schedules from host-side demand, per its descriptor row. A
// DemandTrackerProbe kind counts against ISSUE_TRACKER, resolved with the
// tracker's own knobs and the work labels from the same settings a child uses
// (a blank ISSUE_TRACKER is github, as the launcher defaults it); an unknown
// tracker or a missing knob yields no source, never an error, and the kind then
// stays exit-driven. A DemandHostOutbox kind always gets its outbox counter: it
// reads only the host filesystem, so a misconfigured tracker cannot take it
// away. A DemandChildReported kind never gets one. The second result names, per
// tracker-probed kind left without a source, why (never a token or secret
// value); warnUnprobedKinds reports it.
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
	sources := demandSources{}
	for _, k := range kinds {
		d, ok := dispatchkind.ByVerb(string(k))
		if !ok {
			continue
		}
		switch d.DemandSource {
		case dispatchkind.DemandTrackerProbe:
			probed = append(probed, d)
		case dispatchkind.DemandHostOutbox:
			sources[daemon.KindOf(d)] = newOutboxDemand(doc)
		case dispatchkind.DemandChildReported:
			// No host-side source: only a child run knows, so the kind stays
			// exit-driven.
		default:
			panic(fmt.Sprintf("daemon: kind %s has unhandled DemandSource %d", k, d.DemandSource))
		}
	}
	if len(probed) == 0 {
		return sources, nil
	}
	tracked, missing := buildTrackerSources(doc, probed)
	maps.Copy(sources, tracked)
	return sources, missing
}

// buildTrackerSources is the DemandTrackerProbe half of buildDemandSources.
func buildTrackerSources(doc *inputdoc.Document, probed []*dispatchkind.Descriptor) (demandSources, missingSources) {
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

// outboxDemandInterval paces the outbox scan: a local directory read, so as
// cheap as the local tracker's.
const outboxDemandInterval = 20 * time.Second

// idReader is a demand source that names its ready items, not only counts them.
type idReader interface{ ReadyIDs() ([]string, error) }

// outboxDemand answers a DemandHostOutbox kind's Demand probe from the host
// filesystem alone: no tracker call and no child (ADR 0059). The count is an
// upper bound (recoverrecord.Eligible), so a child may still exit 2.
type outboxDemand struct {
	maxAttempts int
	backoffUnit time.Duration
	now         func() time.Time
}

// newOutboxDemand resolves the attempt bound and backoff as a recover child
// does; the outbox is read relative to the daemon's cwd, which every child
// inherits (RunChild sets no cmd.Dir).
func newOutboxDemand(doc *inputdoc.Document) *outboxDemand {
	return &outboxDemand{
		maxAttempts: launcherInt(doc, "MAX_RECOVER_ATTEMPTS", os.Getenv("MAX_RECOVER_ATTEMPTS")),
		backoffUnit: time.Duration(launcherInt(doc, "TRANSIENT_BACKOFF_SECS", os.Getenv("TRANSIENT_BACKOFF_SECS"))) * time.Second,
		now:         time.Now,
	}
}

// ReadyIDs names each eligible bundle by outbox key and BundleID.
func (o *outboxDemand) ReadyIDs() ([]string, error) {
	return recoverrecord.Eligible("", o.maxAttempts, o.backoffUnit, o.now())
}

func (o *outboxDemand) CountReady(bool) (int, error) {
	ids, err := o.ReadyIDs()
	return len(ids), err
}

func (o *outboxDemand) ProbeInterval() time.Duration { return outboxDemandInterval }

// launcherInt resolves key as the launcher's atoiSchema does for a child: a
// positive integer, else the default, which is the document's own value for
// key parsed with strconv.Atoi (so a document "0" or an unparseable value is
// 0, not the schema default) or, absent from the document, the schema default.
// A document value that counts as a setting strips the ambient env from the
// child, so it never competes with ambient. Keep in step with atoi,
// atoiSchema and schemaDefault in cmd/launcher/main.go.
func launcherInt(doc *inputdoc.Document, key, ambient string) int {
	def := inputdoc.SchemaDefault(key)
	if doc != nil {
		if v, ok := doc.Settings[key]; ok {
			def = v
		}
		if _, counted := doc.Setting(key); counted {
			ambient = ""
		}
	}
	d, _ := strconv.Atoi(def)
	if n, err := strconv.Atoi(ambient); err == nil && n > 0 {
		return n
	}
	return d
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

// trackers is daemon.Config.Trackers for src: every kind with a Demand source
// counts against the one ISSUE_TRACKER, so they share a rate-limit pause (ADR
// 0059). The outbox kind shares it although its count never calls the tracker:
// each child it starts does (ListIssues, label swaps, merge), so it must not
// keep starting while the tracker is paused.
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
	codeForge, boxAccess                          string
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
	k.codeForge = childKnob(doc, "CODE_FORGE", os.Getenv("CODE_FORGE"))
	k.boxAccess = childKnob(doc, "BOX_FORGE_AND_ISSUE_ACCESS", os.Getenv("BOX_FORGE_AND_ISSUE_ACCESS"))
	return k, nil
}

// daemonOnlyRaw are the raw values of the daemon-only knobs
// (inputdoc.IsDaemonOnly). selfApp and researchReservation stay empty when
// their gate is off.
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
