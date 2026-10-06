package main

import (
	"net/http"
	"os"
	"time"

	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/github"
	"spindrift.dev/launcher/internal/forge/jira"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/inputdoc"
)

// jiraProbeTimeout bounds each Jira Demand probe: the adapter otherwise falls
// back to the untimed http.DefaultClient, and a server that accepts the
// connection but never answers would stall the kind's probe for good.
const jiraProbeTimeout = 30 * time.Second

// demandSources maps each kind the daemon schedules from tracker demand
// (ADR 0059) to the counter that answers its Demand probe. A kind absent
// from the map stays exit-driven.
type demandSources map[daemon.Kind]forge.DemandCounter

// buildDemandSources builds a demand counter for each kind in kinds whose
// descriptor row says DemandTrackerProbe, from the same settings a child
// resolves ISSUE_TRACKER, the tracker's own knobs and the work labels from; a
// blank ISSUE_TRACKER is github, as the launcher defaults it. An unknown
// tracker or a missing knob yields no source, never an error: the kind then
// simply stays exit-driven.
//
// FORGEJO_TOKEN and JIRA_TOKEN are read as plain knobs; a token supplied only
// through its -file or -cmd form is invisible here, so that deployment stays
// exit-driven.
//
// A relative LOCAL_ISSUES_DIR is used as written: it resolves against the
// daemon's cwd, which every child inherits (RunChild sets no cmd.Dir), so
// both sides read the same directory.
//
// The github probe runs `gh` in the daemon's own environment, so it sees the
// daemon's GH_TOKEN; mainRun keeps that fresh from GH_TOKEN_REFRESH_FILE the
// way a child does.
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

	var newTracker func(forge.DispatchLabels) forge.IssueTracker
	switch issueTrackerName(doc) {
	case "local":
		dir := childKnob(doc, "LOCAL_ISSUES_DIR", os.Getenv("LOCAL_ISSUES_DIR"))
		if dir == "" {
			return nil
		}
		newTracker = func(l forge.DispatchLabels) forge.IssueTracker { return local.NewLocalTracker(dir, l) }
	case "forgejo":
		baseURL := childKnob(doc, "FORGEJO_BASE_URL", os.Getenv("FORGEJO_BASE_URL"))
		repo := childKnob(doc, "REPO_SLUG", os.Getenv("REPO_SLUG"))
		token := childKnob(doc, "FORGEJO_TOKEN", os.Getenv("FORGEJO_TOKEN"))
		if repo == "" || token == "" {
			return nil
		}
		newTracker = func(l forge.DispatchLabels) forge.IssueTracker {
			return forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: baseURL, Repo: repo, Token: token, Labels: l})
		}
	case "jira":
		baseURL := childKnob(doc, "JIRA_BASE_URL", os.Getenv("JIRA_BASE_URL"))
		projectKey := childKnob(doc, "JIRA_PROJECT_KEY", os.Getenv("JIRA_PROJECT_KEY"))
		token := childKnob(doc, "JIRA_TOKEN", os.Getenv("JIRA_TOKEN"))
		rawMapping := childKnob(doc, "JIRA_STATUS_MAPPING", os.Getenv("JIRA_STATUS_MAPPING"))
		// The child's validate() runs the same check and fails startup on it, so
		// a deployment it rejects has no children to count demand for. The check
		// parses rawMapping too, so the parse below cannot fail.
		if jira.ValidateJiraEnv(baseURL, projectKey, token, rawMapping) != nil {
			return nil
		}
		statusMapping, _ := jira.ParseStatusMapping(rawMapping)
		email := childKnob(doc, "JIRA_EMAIL", os.Getenv("JIRA_EMAIL"))
		newTracker = func(l forge.DispatchLabels) forge.IssueTracker {
			return jira.NewJiraClient(jira.JiraConfig{
				BaseURL: baseURL, ProjectKey: projectKey, Email: email, Token: token,
				StatusMapping: statusMapping, Labels: l,
				HTTPClient: &http.Client{Timeout: jiraProbeTimeout},
			})
		}
	case "github":
		slug := childKnob(doc, "REPO_SLUG", os.Getenv("REPO_SLUG"))
		if slug == "" {
			return nil
		}
		// The branch prefix only names agent branches, which counting never touches.
		newTracker = func(l forge.DispatchLabels) forge.IssueTracker { return github.NewExecClient(slug, l, "") }
	default:
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
		counter, ok := newTracker(labels).(forge.DemandCounter)
		if !ok {
			continue
		}
		sources[daemon.KindOf(d)] = counter
	}
	return sources
}

// issueTrackerName is the ISSUE_TRACKER a child resolves, a blank one being
// github as the launcher defaults it.
func issueTrackerName(doc *inputdoc.Document) string {
	if name := childKnob(doc, "ISSUE_TRACKER", os.Getenv("ISSUE_TRACKER")); name != "" {
		return name
	}
	return "github"
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
