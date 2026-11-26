package main

import (
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/recoverrecord"
	"spindrift.dev/launcher/internal/report"
)

// demandSources maps each kind the daemon schedules from host-side demand
// (ADR 0059: a tracker probe or the outbox count) to the counter that answers
// its Demand probe. A kind absent from the map stays exit-driven.
type demandSources map[daemon.Kind]forge.DemandCounter

// missingSources maps each probed kind that got no Demand source to the reason
// it stays exit-driven.
type missingSources map[daemon.Kind]string

// buildDemandSources builds each kind's Demand counter per its descriptor
// row. A DemandTrackerProbe kind counts against ISSUE_TRACKER, resolved from
// the same settings a child uses; an unknown tracker or missing knob leaves
// it exit-driven, never an error. A DemandHostOutbox kind always gets its
// outbox counter: it reads only the host filesystem, so a misconfigured
// tracker cannot take it away. The second result says why each probed kind
// got no source (never a secret value).
//
// Tracker tokens are read as plain knobs, so one supplied only through its
// -file/-cmd flag or _CMD form leaves the kind exit-driven (a set _CMD earns a
// tokenFormHint). A relative LOCAL_ISSUES_DIR resolves against the daemon's
// cwd, which every child inherits (StartChild sets no cmd.Dir).
func buildDemandSources(connectors *connectorruntime.Runtime, doc *inputdoc.Document, kinds []daemon.Kind) (demandSources, missingSources) {
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
			// Exit-driven: only a child run knows its demand.
		default:
			panic(fmt.Sprintf("daemon: kind %s has unhandled DemandSource %d", k, d.DemandSource))
		}
	}
	if len(probed) == 0 {
		return sources, nil
	}
	tracked, missing := buildTrackerSources(connectors, doc, probed)
	maps.Copy(sources, tracked)
	return sources, missing
}

// buildTrackerSources is the DemandTrackerProbe half of buildDemandSources.
func buildTrackerSources(connectors *connectorruntime.Runtime, doc *inputdoc.Document, probed []*dispatchkind.Descriptor) (demandSources, missingSources) {
	name := issueTrackerName(doc)
	strat, ok := backendkit.ByName(name)
	if !ok {
		return nil, allMissing(probed, fmt.Sprintf("unknown ISSUE_TRACKER %q (valid: %s)", name, strings.Join(trackerNames(), ", ")))
	}
	if strat.NewIssueTracker == nil {
		return nil, allMissing(probed, fmt.Sprintf("%q is not a valid ISSUE_TRACKER (valid: %s)", name, strings.Join(trackerNames(), ", ")))
	}
	lookup := childLookup(doc)
	// The branch prefix is left blank: it only names agent branches, which
	// counting never touches.
	shared := backendkit.Shared{RepoSlug: childKnob(doc, "REPO_SLUG", os.Getenv("REPO_SLUG")), Connectors: connectors}
	// A route the child would refuse must not leave the daemon counting the
	// tracker's whole scope.
	route, err := forge.ResolveRoute(issueRoute(doc), name, strat.RouteKinds)
	if err != nil {
		return nil, allMissing(probed, err.Error())
	}
	shared.Route = route
	// Validation runs without a Tracker, so it sees the raw mapping and a
	// malformed one is rejected even when only the research kind (which ignores
	// it) is probed.
	if strat.ValidateTracker != nil {
		if err := strat.ValidateTracker(shared, lookup); err != nil {
			return nil, allMissing(probed, err.Error())
		}
	}
	// The row-credential rule launcherchecks applies to a selected backend:
	// without it NewIssueTracker fails with a Connector-construction error that
	// never names the knob. In production the connector runtime already
	// refused an unset credential for every Connector-routed row, so this is the
	// fallback that keeps the probe's reason naming the knob. A row whose
	// runtime source holds a token (a `_CMD` form) is set whatever the knob says.
	if env := strat.TokenEnvVar; env != "" {
		if v, _ := lookup(env); v == "" && !rowCredentialSet(connectors, strat.Name) {
			return nil, allMissing(probed, fmt.Sprintf("set %s, the credential of the %s backend%s", env, strat.Name, tokenFormHint(env, lookup)))
		}
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
		shared.Labels = labels
		shared.Tracker = d.Tracker
		tracker, err := strat.NewIssueTracker(shared, lookup)
		if err != nil {
			missing[daemon.KindOf(d)] = err.Error()
			continue
		}
		// Only the tracker's capabilities matter: no code forge is in play, and the
		// descriptors feed fields counting never reads.
		counter := forge.ResolveCapabilities(nil, tracker, backend.Descriptor{}, backend.Descriptor{}).DemandCounter
		if counter == nil {
			missing[daemon.KindOf(d)] = fmt.Sprintf("ISSUE_TRACKER=%s has no Demand adapter", name)
			continue
		}
		sources[daemon.KindOf(d)] = counter
	}
	return sources, missing
}

// trackerNames lists the registered backends that build an Issue Tracker, in
// registry order.
func trackerNames() []string {
	var names []string
	for _, s := range backendkit.Strategies() {
		if s.NewIssueTracker != nil {
			names = append(names, s.Name)
		}
	}
	return names
}

// childEnvValue is the daemon's backendkit.Lookup body, resolving key as a
// child does. The Strategy names its own knobs, so the key is not a literal;
// clearKnobEnvT records the keys the registered Strategies read.
func childEnvValue(doc *inputdoc.Document, key string) (string, bool) {
	return childValue(doc, key, os.Getenv(key))
}

// backendRowSetting resolves the knob a backend row names as its baseEnv: the
// ambient env first, then the document. The launcher's backendRowSetting
// (inputdoc.go) goes through flag, document and default instead. The
// allowlist in knob_env_guard_test.go says why the key is not a literal.
func backendRowSetting(doc *inputdoc.Document, stderr io.Writer, key string) string {
	v, _ := doc.Lookup(key, stderr)
	return v
}

// childLookup is the Lookup a backend Strategy resolves its knobs through.
// A knob unset in both places reports absent, so the backend's generated
// ReadKnobs applies its own row default.
func childLookup(doc *inputdoc.Document) backendkit.Lookup {
	return func(key string) (string, bool) { return childEnvValue(doc, key) }
}

// outboxDemandInterval paces the outbox scan: a local directory read, so as
// cheap as the local tracker's.
const outboxDemandInterval = 20 * time.Second

// idReader is a demand source that names its ready items, not only counts them.
type idReader interface {
	ReadyIDs(inFlight dispatchkey.Set) ([]string, error)
}

// outboxDemand answers a DemandHostOutbox kind's Demand probe from the host
// filesystem alone: no tracker call and no child (ADR 0059). The count is an
// upper bound (recoverrecord.Eligible), so a child may still exit 2. It leaves
// out the keys the pool's own children hold.
type outboxDemand struct {
	maxAttempts int
	backoffUnit time.Duration
	now         func() time.Time
}

// newOutboxDemand resolves the attempt bound and backoff as a recover child
// does; the outbox is read relative to the daemon's cwd, which every child
// inherits (StartChild sets no cmd.Dir).
func newOutboxDemand(doc *inputdoc.Document) *outboxDemand {
	return &outboxDemand{
		maxAttempts: launcherInt(doc, "MAX_RECOVER_ATTEMPTS", os.Getenv("MAX_RECOVER_ATTEMPTS")),
		backoffUnit: time.Duration(launcherInt(doc, "TRANSIENT_BACKOFF_SECS", os.Getenv("TRANSIENT_BACKOFF_SECS"))) * time.Second,
		now:         time.Now,
	}
}

// ReadyIDs names each eligible bundle by outbox key and BundleID, leaving out
// the bundles of inFlight keys.
func (o *outboxDemand) ReadyIDs(inFlight dispatchkey.Set) ([]string, error) {
	return recoverrecord.Eligible("", o.maxAttempts, o.backoffUnit, o.now(), inFlight)
}

func (o *outboxDemand) CountReady(bool) (int, error) {
	ids, err := o.ReadyIDs(nil)
	return len(ids), err
}

func (o *outboxDemand) ProbeInterval() time.Duration { return outboxDemandInterval }

// launcherInt resolves key as a child's knobResolver.PositiveInt
// (internal/inputdoc/resolve.go) does; keep the two in step. A positive
// ambient value wins unless the document's value counts as a setting, which
// strips the ambient one from the child's env; otherwise the result is the
// document's value parsed as-is (a "0" or unparseable value is 0), else the
// schema default.
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

// tokenFormHint explains a blank plain token when its _CMD form is set, since
// the daemon never runs the command. It names knobs only.
func tokenFormHint(plain string, lookup backendkit.Lookup) string {
	if cmd, _ := lookup(plain + "_CMD"); cmd == "" {
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
		em.Emit(daemon.Event{Event: report.EventDemandSourceMissing, Kind: k, Reason: missing[k]})
	}
}

// probeRoute runs each tracker-probed kind's composed ready query once, fresh,
// when ISSUE_ROUTE is set, and logs how many issues the route matches (zero is
// logged, not an error). A tracker that rejects the query fails startup naming
// the route, before the pool exists, rather than starving silently on every
// probe. Unrouted, it does nothing. A route is not a secret, so the error may
// carry its value.
func probeRoute(demand demandSources, kinds []daemon.Kind, route string, stderr io.Writer) error {
	route = strings.TrimSpace(route)
	if route == "" {
		return nil
	}
	for _, k := range kinds {
		d, ok := dispatchkind.ByVerb(string(k))
		counter := demand[k]
		if !ok || counter == nil {
			continue
		}
		// Exhaustive like buildDemandSources, so a new DemandSource cannot be
		// skipped silently here.
		switch d.DemandSource {
		case dispatchkind.DemandTrackerProbe:
		case dispatchkind.DemandHostOutbox, dispatchkind.DemandChildReported:
			// Only a tracker counter runs the routed query.
			continue
		default:
			panic(fmt.Sprintf("daemon: kind %s has unhandled DemandSource %d", k, d.DemandSource))
		}
		n, err := counter.CountReady(true)
		if err != nil {
			return fmt.Errorf("%s: ISSUE_ROUTE %q: tracker rejected the ready query: %w", k, route, err)
		}
		fmt.Fprintf(stderr, "daemon: %s: ISSUE_ROUTE %q matches %d ready issue(s)\n", k, route, n)
		if it, ok := counter.(forge.IssueTracker); ok {
			warnDoneComplete(k, it, stderr)
		}
	}
	return nil
}

// warnDoneComplete warns when the status the tracker maps Complete to is in the
// Done category, for the reason forge.StatusCategoryReader gives. It only
// warns; the tracker may not expose the capability, and a failed read is noted,
// never fatal.
func warnDoneComplete(k daemon.Kind, it forge.IssueTracker, stderr io.Writer) {
	r, ok := forge.As[forge.StatusCategoryReader](it)
	if !ok {
		return
	}
	category, err := r.MappedStatusCategory(forge.Complete)
	if err != nil {
		fmt.Fprintf(stderr, "daemon: %s: could not check the complete status's category: %v\n", k, err)
		return
	}
	if category == forge.StatusCategoryDone {
		fmt.Fprintf(stderr, "daemon: %s: warning: the complete status is in the Done category; with ISSUE_ROUTE set, a green PR still awaiting review closes the issue and releases cross-route dependents before it merges (map complete to a status outside the Done category, such as a review status, and let the tracker close the issue on merge)\n", k)
	}
}

// issueRoute is the ISSUE_ROUTE a child resolves.
func issueRoute(doc *inputdoc.Document) string {
	return childKnob(doc, "ISSUE_ROUTE", os.Getenv("ISSUE_ROUTE"))
}

// issueTrackerName is the ISSUE_TRACKER a child resolves.
func issueTrackerName(doc *inputdoc.Document) string {
	return childKnob(doc, "ISSUE_TRACKER", os.Getenv("ISSUE_TRACKER"))
}

// codeForgeName is the CODE_FORGE a child resolves.
func codeForgeName(doc *inputdoc.Document) string {
	return childKnob(doc, "CODE_FORGE", os.Getenv("CODE_FORGE"))
}

// trackers is daemon.Config.Trackers for src: every sourced kind shares the
// one ISSUE_TRACKER's rate-limit pause (ADR 0059). The outbox kind too: its
// count never calls the tracker, but each child it starts does, so it must not
// keep starting them while the tracker is paused.
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

// childKnob is the value a child launcher resolves for key: the document's
// value when it counts (withoutKeys then strips the ambient one from the
// child), else the inherited ambient value, else the schema default. Callers
// pass ambient as a literal os.Getenv so knob_env_guard_test.go sees which
// keys the daemon reads.
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
	k.codeForge = codeForgeName(doc)
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

// repoURL is the Target repo's web URL for the ISSUE_TRACKER, or "". It
// follows the tracker, not CODE_FORGE, because it feeds <repo>/issues/<n>
// links.
func repoURL(doc *inputdoc.Document) string {
	slug := childKnob(doc, "REPO_SLUG", os.Getenv("REPO_SLUG"))
	if slug == "" {
		return ""
	}
	s, ok := backendkit.ByName(issueTrackerName(doc))
	if !ok || s.WebURL == nil {
		return ""
	}
	return s.WebURL(backendkit.Shared{RepoSlug: slug}, childLookup(doc))
}

// rowCredentialSet is whether the runtime holds a credential for the named
// backend row.
func rowCredentialSet(connectors *connectorruntime.Runtime, name string) bool {
	if connectors == nil {
		return false
	}
	row, ok := connectors.Row(name)
	return ok && connectors.CredentialSet(row)
}
