package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/inputdoc"
)

// writeIssueFileT drops one local-tracker issue file in dir whose
// frontmatter state is state.
func writeIssueFileT(t *testing.T, dir, slug, state string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\ntitle: " + slug + "\nstate: " + state + "\nlabels: []\ncreated: 2024-01-01T00:00:00Z\n---\nbody\n"
	writeFileT(t, filepath.Join(dir, slug+".md"), body)
}

func demandDocT(settings map[string]string) *inputdoc.Document {
	return &inputdoc.Document{Settings: settings}
}

var allDemandKinds = []daemon.Kind{
	daemon.KindOf(dispatchkind.Work),
	daemon.KindOf(dispatchkind.Research),
	daemon.KindOf(dispatchkind.Butler),
}

func TestBuildDemandSources_LocalTrackerPerKindLabel(t *testing.T) {
	clearKnobEnvT(t)
	dir := t.TempDir()
	writeIssueFileT(t, dir, "w1", "ready-for-agent")
	writeIssueFileT(t, dir, "w2", "ready-for-agent")
	writeIssueFileT(t, dir, "busy", "agent-in-progress")
	writeIssueFileT(t, dir, "r1", "agent-research")

	doc := demandDocT(map[string]string{
		"ISSUE_TRACKER":    "local",
		"LOCAL_ISSUES_DIR": dir,
		"LABEL":            "ready-for-agent",
	})
	src, _ := buildDemandSources(doc, allDemandKinds)

	if _, ok := src[daemon.KindOf(dispatchkind.Butler)]; ok {
		t.Error("butler (DemandChildReported) got a tracker demand source")
	}
	for kind, want := range map[daemon.Kind]int{
		daemon.KindOf(dispatchkind.Work):     2,
		daemon.KindOf(dispatchkind.Research): 1,
	} {
		c, ok := src[kind]
		if !ok {
			t.Fatalf("no demand source for %s", kind)
		}
		got, err := c.CountReady(false)
		if err != nil || got != want {
			t.Errorf("%s CountReady() = %d, %v; want %d, nil", kind, got, err, want)
		}
		if c.ProbeInterval() <= 0 {
			t.Errorf("%s ProbeInterval() = %v, want > 0", kind, c.ProbeInterval())
		}
	}
}

func TestBuildDemandSources_ConfiguredWorkLabel(t *testing.T) {
	clearKnobEnvT(t)
	dir := t.TempDir()
	writeIssueFileT(t, dir, "a", "go-agent")
	writeIssueFileT(t, dir, "b", "ready-for-agent")
	src, _ := buildDemandSources(demandDocT(map[string]string{
		"ISSUE_TRACKER":    "local",
		"LOCAL_ISSUES_DIR": dir,
		"LABEL":            "go-agent",
	}), allDemandKinds)
	got, err := src[daemon.KindOf(dispatchkind.Work)].CountReady(false)
	if err != nil || got != 1 {
		t.Errorf("CountReady() = %d, %v; want 1, nil", got, err)
	}
}

// A relative LOCAL_ISSUES_DIR must resolve against the daemon's cwd, which
// is the cwd every child inherits (RunChild sets no cmd.Dir) and the base the
// launcher's own local.NewLocalTracker resolves it against.
func TestBuildDemandSources_RelativeDirResolvesAgainstCwd(t *testing.T) {
	clearKnobEnvT(t)
	cwd := t.TempDir()
	writeIssueFileT(t, filepath.Join(cwd, ".spindrift", "issues"), "x", "ready-for-agent")
	t.Chdir(cwd)

	for _, dir := range []string{".spindrift/issues", "./.spindrift/issues"} {
		src, _ := buildDemandSources(demandDocT(map[string]string{
			"ISSUE_TRACKER":    "local",
			"LOCAL_ISSUES_DIR": dir,
			"LABEL":            "ready-for-agent",
		}), allDemandKinds)
		got, err := src[daemon.KindOf(dispatchkind.Work)].CountReady(false)
		if err != nil || got != 1 {
			t.Errorf("dir %q: CountReady() = %d, %v; want 1, nil", dir, got, err)
		}
	}
}

// github (the launcher's default tracker, so a blank ISSUE_TRACKER too) builds
// a gh-backed source per probed kind. CountReady is never called: it would
// reach the real GitHub.
func TestBuildDemandSources_GitHubBuildsProbeSource(t *testing.T) {
	clearKnobEnvT(t)
	for name, tracker := range map[string]string{"github": "github", "blank": ""} {
		t.Run(name, func(t *testing.T) {
			src, _ := buildDemandSources(demandDocT(map[string]string{
				"ISSUE_TRACKER": tracker, "REPO_SLUG": "o/r", "LABEL": "ready-for-agent",
			}), allDemandKinds)
			if len(src) != 2 {
				t.Fatalf("sources = %v, want work and research", src)
			}
			for kind, c := range src {
				if got := c.ProbeInterval(); got != time.Minute {
					t.Errorf("%s ProbeInterval() = %v, want 1m", kind, got)
				}
			}
		})
	}
}

func TestBuildDemandSources_GitHubReadsAmbientRepoSlug(t *testing.T) {
	clearKnobEnvT(t)
	t.Setenv("REPO_SLUG", "o/r")
	src, _ := buildDemandSources(demandDocT(map[string]string{
		"ISSUE_TRACKER": "github", "LABEL": "ready-for-agent",
	}), allDemandKinds)
	if len(src) != 2 {
		t.Errorf("sources = %v, want work and research", src)
	}
}

// jira builds a source per probed kind that counts through a zero-row search
// whose JQL is the one ListIssues would run: the project, the status mapping
// and the kind's own dispatchable label.
func TestBuildDemandSources_JiraCountsWithStatusMappingAndPerKindLabel(t *testing.T) {
	clearKnobEnvT(t)
	var mu sync.Mutex
	var jqls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/search" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		jqls = append(jqls, r.URL.Query().Get("jql"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"total": 7, "issues": []}`))
	}))
	defer srv.Close()

	src, _ := buildDemandSources(demandDocT(map[string]string{
		"ISSUE_TRACKER":       "jira",
		"JIRA_BASE_URL":       srv.URL,
		"JIRA_PROJECT_KEY":    "PROJ",
		"JIRA_TOKEN":          "tok",
		"JIRA_STATUS_MAPPING": `{"dispatchable":"To Do"}`,
		"LABEL":               "ready-for-agent",
	}), allDemandKinds)
	if len(src) != 2 {
		t.Fatalf("sources = %v, want work and research", src)
	}
	for kind, label := range map[daemon.Kind]string{
		daemon.KindOf(dispatchkind.Work):     "ready-for-agent",
		daemon.KindOf(dispatchkind.Research): "agent-research",
	} {
		c, ok := src[kind]
		if !ok {
			t.Fatalf("no demand source for %s", kind)
		}
		if got := c.ProbeInterval(); got != 5*time.Minute {
			t.Errorf("%s ProbeInterval() = %v, want 5m", kind, got)
		}
		mu.Lock()
		jqls = nil
		mu.Unlock()
		got, err := c.CountReady(false)
		if err != nil || got != 7 {
			t.Errorf("%s CountReady() = %d, %v; want 7, nil", kind, got, err)
		}
		mu.Lock()
		seen := append([]string(nil), jqls...)
		mu.Unlock()
		if len(seen) != 1 {
			t.Fatalf("%s: %d searches, want 1", kind, len(seen))
		}
		for _, want := range []string{`project = "PROJ"`, `status = "To Do"`, `labels = "` + label + `"`} {
			if !strings.Contains(seen[0], want) {
				t.Errorf("%s JQL %q lacks %s", kind, seen[0], want)
			}
		}
	}
}

func TestBuildDemandSources_NoAdapterOrMissingKnobsHasNoSource(t *testing.T) {
	clearKnobEnvT(t)
	for name, settings := range map[string]map[string]string{
		"github without slug":                 {"ISSUE_TRACKER": "github", "LABEL": "ready-for-agent"},
		"jira without base URL/project/token": {"ISSUE_TRACKER": "jira", "REPO_SLUG": "o/r", "LABEL": "ready-for-agent"},
		"jira without token":                  {"ISSUE_TRACKER": "jira", "JIRA_BASE_URL": "http://x", "JIRA_PROJECT_KEY": "P", "LABEL": "ready-for-agent"},
		"jira with malformed status mapping":  {"ISSUE_TRACKER": "jira", "JIRA_BASE_URL": "http://x", "JIRA_PROJECT_KEY": "P", "JIRA_TOKEN": "t", "JIRA_STATUS_MAPPING": "garbage", "LABEL": "ready-for-agent"},
		"forgejo":                             {"ISSUE_TRACKER": "forgejo", "REPO_SLUG": "o/r", "LABEL": "ready-for-agent"},
		"unknown tracker":                     {"ISSUE_TRACKER": "bogus", "REPO_SLUG": "o/r", "LABEL": "ready-for-agent"},
		"absent":                              {},
	} {
		t.Run(name, func(t *testing.T) {
			src, _ := buildDemandSources(demandDocT(settings), allDemandKinds)
			if len(src) != 0 {
				t.Errorf("sources = %v, want none (kind stays exit-driven)", src)
			}
		})
	}
}

// A knob the --input document carries is stripped from every child's env, so
// the child reads the document alone; an ambient override of it must not
// point the daemon's count at a different directory or label.
func TestBuildDemandSources_AmbientOverrideOfDocumentKnobIgnored(t *testing.T) {
	clearKnobEnvT(t)
	docDir, ambientDir := t.TempDir(), t.TempDir()
	writeIssueFileT(t, docDir, "a", "ready-for-agent")
	writeIssueFileT(t, docDir, "z", "ambient-label")
	writeIssueFileT(t, ambientDir, "x", "ready-for-agent")
	writeIssueFileT(t, ambientDir, "y", "ready-for-agent")
	t.Setenv("LOCAL_ISSUES_DIR", ambientDir)
	t.Setenv("LABEL", "ambient-label")
	t.Setenv("ISSUE_TRACKER", "github")

	src, _ := buildDemandSources(demandDocT(map[string]string{
		"ISSUE_TRACKER":    "local",
		"LOCAL_ISSUES_DIR": docDir,
		"LABEL":            "ready-for-agent",
	}), allDemandKinds)
	got, err := src[daemon.KindOf(dispatchkind.Work)].CountReady(false)
	if err != nil || got != 1 {
		t.Errorf("CountReady() = %d, %v; want 1 (the document's directory and label), nil", got, err)
	}
}

// A knob absent from the document is not stripped, so the child inherits the
// ambient value and the daemon must read it too.
func TestBuildDemandSources_AmbientKnobAbsentFromDocumentHonoured(t *testing.T) {
	clearKnobEnvT(t)
	dir := t.TempDir()
	writeIssueFileT(t, dir, "a", "ready-for-agent")
	t.Setenv("LOCAL_ISSUES_DIR", dir)

	src, _ := buildDemandSources(demandDocT(map[string]string{
		"ISSUE_TRACKER": "local",
		"LABEL":         "ready-for-agent",
	}), allDemandKinds)
	got, err := src[daemon.KindOf(dispatchkind.Work)].CountReady(false)
	if err != nil || got != 1 {
		t.Errorf("CountReady() = %d, %v; want 1, nil", got, err)
	}
}

func TestBuildDemandSources_OnlyConfiguredKinds(t *testing.T) {
	clearKnobEnvT(t)
	src, _ := buildDemandSources(demandDocT(map[string]string{
		"ISSUE_TRACKER": "local", "LOCAL_ISSUES_DIR": t.TempDir(), "LABEL": "ready-for-agent",
	}), []daemon.Kind{daemon.KindOf(dispatchkind.Research)})
	if len(src) != 1 {
		t.Fatalf("sources = %v, want only research", src)
	}
	if _, ok := src[daemon.KindOf(dispatchkind.Research)]; !ok {
		t.Error("research source missing")
	}
}

func TestParseProbeInterval(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{name: "blank means per-tracker defaults", raw: "", want: 0},
		{name: "seconds", raw: "30s", want: 30 * time.Second},
		{name: "one second is the floor", raw: "1s", want: time.Second},
		{name: "zero rejected", raw: "0s", wantErr: true},
		{name: "sub-second rejected", raw: "999ms", wantErr: true},
		{name: "nanosecond rejected", raw: "1ns", wantErr: true},
		{name: "negative rejected", raw: "-1s", wantErr: true},
		{name: "garbage rejected", raw: "soon", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseProbeInterval(tt.raw)
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "DAEMON_PROBE_INTERVAL") {
					t.Fatalf("parseProbeInterval(%q) = %v, %v; want an error naming DAEMON_PROBE_INTERVAL", tt.raw, got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("parseProbeInterval(%q) = %v, %v; want %v, nil", tt.raw, got, err, tt.want)
			}
		})
	}
}

type fixedInterval time.Duration

func (fixedInterval) CountReady(bool) (int, error)   { return 0, nil }
func (f fixedInterval) ProbeInterval() time.Duration { return time.Duration(f) }

func TestProbeIntervals(t *testing.T) {
	work, research := daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research)
	src := demandSources{work: fixedInterval(20 * time.Second), research: fixedInterval(time.Minute)}

	got := probeIntervals(src, 0)
	if got[work] != 20*time.Second || got[research] != time.Minute || len(got) != 2 {
		t.Errorf("probeIntervals(no override) = %v", got)
	}
	got = probeIntervals(src, 7*time.Second)
	if got[work] != 7*time.Second || got[research] != 7*time.Second {
		t.Errorf("probeIntervals(override) = %v, want 7s for every kind", got)
	}
	if got := probeIntervals(nil, time.Second); len(got) != 0 {
		t.Errorf("probeIntervals(no sources) = %v, want empty (an override never creates a source)", got)
	}
}

func TestHostRunnerDemand(t *testing.T) {
	clearKnobEnvT(t)
	dir := t.TempDir()
	writeIssueFileT(t, dir, "a", "ready-for-agent")
	src, _ := buildDemandSources(demandDocT(map[string]string{
		"ISSUE_TRACKER": "local", "LOCAL_ISSUES_DIR": dir, "LABEL": "ready-for-agent",
	}), allDemandKinds)
	r := mustHostRunner(t, hostRunnerConfig{env: []string{}, demand: src})

	d, err := r.Demand(context.Background(), daemon.KindOf(dispatchkind.Work), false)
	if err != nil || d.Ready != 1 {
		t.Errorf("Demand(work) = %+v, %v; want Ready 1, nil", d, err)
	}
	if _, err := r.Demand(context.Background(), daemon.KindOf(dispatchkind.Butler), false); err == nil {
		t.Error("Demand(butler) = nil error, want an error: no demand source")
	}
}

func TestMainRun_BadDaemonProbeIntervalFailsStartup(t *testing.T) {
	clearKnobEnvT(t)
	docPath := writeInputDocT(t, map[string]string{
		"DAEMON_APP":            ".#dogfood",
		"BASE_BRANCH":           "main",
		"MAX_PARALLEL":          "1",
		"DAEMON_PROBE_INTERVAL": "soon",
	})
	var stdout, stderr bytes.Buffer
	if got := mainRun([]string{"--input", docPath, "dispatch"}, &stdout, &stderr); got != 1 {
		t.Fatalf("mainRun() = %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "DAEMON_PROBE_INTERVAL") {
		t.Errorf("stderr = %q, want it to name DAEMON_PROBE_INTERVAL", stderr.String())
	}
}

var _ forge.DemandCounter = fixedInterval(0)

// A knob unset in both the document and the environment resolves to its
// schema default, as in a child: LABEL probes work for ready-for-agent,
// LOCAL_ISSUES_DIR is .spindrift/issues under the cwd, and ISSUE_TRACKER is
// github.
func TestBuildDemandSources_UnsetKnobsResolveToSchemaDefaults(t *testing.T) {
	t.Run("LABEL", func(t *testing.T) {
		clearKnobEnvT(t)
		dir := t.TempDir()
		writeIssueFileT(t, dir, "w", "ready-for-agent")
		src, _ := buildDemandSources(demandDocT(map[string]string{
			"ISSUE_TRACKER": "local", "LOCAL_ISSUES_DIR": dir,
		}), allDemandKinds)
		c, ok := src[daemon.KindOf(dispatchkind.Work)]
		if !ok {
			t.Fatal("work has no source though LABEL defaults to ready-for-agent")
		}
		if _, ok := src[daemon.KindOf(dispatchkind.Research)]; !ok {
			t.Error("research has no source")
		}
		if got, err := c.CountReady(false); err != nil || got != 1 {
			t.Errorf("CountReady() = %d, %v; want 1, nil", got, err)
		}
	})
	t.Run("LOCAL_ISSUES_DIR", func(t *testing.T) {
		clearKnobEnvT(t)
		cwd := t.TempDir()
		writeIssueFileT(t, filepath.Join(cwd, ".spindrift", "issues"), "x", "ready-for-agent")
		t.Chdir(cwd)
		src, _ := buildDemandSources(demandDocT(map[string]string{"ISSUE_TRACKER": "local"}), allDemandKinds)
		c, ok := src[daemon.KindOf(dispatchkind.Work)]
		if !ok {
			t.Fatal("work has no source though LOCAL_ISSUES_DIR defaults to .spindrift/issues")
		}
		if got, err := c.CountReady(false); err != nil || got != 1 {
			t.Errorf("CountReady() = %d, %v; want 1, nil", got, err)
		}
	})
	t.Run("ISSUE_TRACKER", func(t *testing.T) {
		clearKnobEnvT(t)
		doc := demandDocT(map[string]string{"REPO_SLUG": "o/r"})
		if got := issueTrackerName(doc); got != "github" {
			t.Fatalf("issueTrackerName() = %q, want github", got)
		}
		src, _ := buildDemandSources(doc, allDemandKinds)
		if len(src) != 2 {
			t.Fatalf("sources = %v, want work and research over the default github tracker", src)
		}
	})
}

func TestBuildDemandSources_ForgejoPerKindSourceWithProbeInterval(t *testing.T) {
	clearKnobEnvT(t)
	src, _ := buildDemandSources(demandDocT(map[string]string{
		"ISSUE_TRACKER": "forgejo",
		"REPO_SLUG":     "owner/repo",
		"FORGEJO_TOKEN": "tok",
		"LABEL":         "ready-for-agent",
	}), allDemandKinds)

	if _, ok := src[daemon.KindOf(dispatchkind.Butler)]; ok {
		t.Error("butler (DemandChildReported) got a tracker demand source")
	}
	for _, kind := range []daemon.Kind{daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research)} {
		c, ok := src[kind]
		if !ok {
			t.Fatalf("no demand source for %s", kind)
		}
		if got := c.ProbeInterval(); got != 3*time.Minute {
			t.Errorf("%s ProbeInterval() = %v, want 3m", kind, got)
		}
	}
}

// The token and slug fall back to the ambient env when the document lacks
// them, as the child inherits them.
func TestBuildDemandSources_ForgejoAmbientKnobsHonoured(t *testing.T) {
	clearKnobEnvT(t)
	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("FORGEJO_TOKEN", "tok")
	src, _ := buildDemandSources(demandDocT(map[string]string{
		"ISSUE_TRACKER": "forgejo", "LABEL": "ready-for-agent",
	}), allDemandKinds)
	if _, ok := src[daemon.KindOf(dispatchkind.Work)]; !ok {
		t.Error("work source missing")
	}
}

func TestBuildDemandSources_ForgejoMissingKnobHasNoSource(t *testing.T) {
	clearKnobEnvT(t)
	for name, settings := range map[string]map[string]string{
		"no token":     {"REPO_SLUG": "owner/repo"},
		"no repo slug": {"FORGEJO_TOKEN": "tok"},
	} {
		t.Run(name, func(t *testing.T) {
			settings["ISSUE_TRACKER"] = "forgejo"
			settings["LABEL"] = "ready-for-agent"
			if src, _ := buildDemandSources(demandDocT(settings), allDemandKinds); len(src) != 0 {
				t.Errorf("sources = %v, want none (kind stays exit-driven)", src)
			}
		})
	}
}

// A token supplied through its _CMD form is invisible to the daemon, so the
// reason must say which knob it reads instead; the command itself stays out.
func TestBuildDemandSources_TokenCmdFormHintNamesKnobNotValue(t *testing.T) {
	work := daemon.KindOf(dispatchkind.Work)
	for _, tc := range []struct {
		name     string
		settings map[string]string
		cmdKnob  string
		want     string
	}{
		{"forgejo", map[string]string{"ISSUE_TRACKER": "forgejo", "REPO_SLUG": "o/r", "FORGEJO_BASE_URL": "http://x"}, "FORGEJO_TOKEN_CMD", "FORGEJO_TOKEN only, not FORGEJO_TOKEN_CMD"},
		{"jira", map[string]string{"ISSUE_TRACKER": "jira", "JIRA_BASE_URL": "http://x", "JIRA_PROJECT_KEY": "P", "JIRA_EMAIL": "a@b"}, "JIRA_TOKEN_CMD", "JIRA_TOKEN only, not JIRA_TOKEN_CMD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearKnobEnvT(t)
			const cmd = "rbw get hush-hush-entry"
			t.Setenv(tc.cmdKnob, cmd)
			tc.settings["LABEL"] = "ready-for-agent"
			_, missing := buildDemandSources(demandDocT(tc.settings), allDemandKinds)
			got := missing[work]
			if !strings.Contains(got, tc.want) {
				t.Errorf("reason = %q, want it to contain %q", got, tc.want)
			}
			if strings.Contains(got, cmd) {
				t.Errorf("reason = %q leaks the command value", got)
			}
		})
	}
}

func TestBuildDemandSources_NoTokenCmdNoHint(t *testing.T) {
	clearKnobEnvT(t)
	_, missing := buildDemandSources(demandDocT(map[string]string{
		"ISSUE_TRACKER": "forgejo", "REPO_SLUG": "o/r", "FORGEJO_BASE_URL": "http://x", "LABEL": "ready-for-agent",
	}), allDemandKinds)
	if got := missing[daemon.KindOf(dispatchkind.Work)]; strings.Contains(got, "_CMD") {
		t.Errorf("reason = %q, want no _CMD hint when the knob is unset", got)
	}
}

func TestTrackers_KindsShareTheIssueTrackerName(t *testing.T) {
	work, research := daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research)
	src := demandSources{work: fixedInterval(time.Second), research: fixedInterval(time.Second)}

	for _, tc := range []struct{ tracker, want string }{
		{"local", "local"},
		{"forgejo", "forgejo"},
		{"github", "github"},
		{"", "github"},
	} {
		clearKnobEnvT(t)
		got := trackers(src, demandDocT(map[string]string{"ISSUE_TRACKER": tc.tracker}))
		if len(got) != 2 || got[work] != tc.want || got[research] != tc.want {
			t.Errorf("ISSUE_TRACKER=%q: trackers = %v, want work and research -> %q", tc.tracker, got, tc.want)
		}
	}
	if got := trackers(nil, demandDocT(nil)); len(got) != 0 {
		t.Errorf("trackers(no sources) = %v, want empty", got)
	}
}

// A document value that is empty does not count as a setting the child reads:
// the child resolves the ambient value, so the daemon's count must too.
func TestBuildDemandSources_EmptyDocumentValueFallsBackToAmbient(t *testing.T) {
	jiraSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"total": 1, "issues": []}`))
	}))
	defer jiraSrv.Close()
	localDir := t.TempDir()

	tests := []struct {
		name    string
		doc     map[string]string
		ambient map[string]string
		check   func(t *testing.T, c forge.DemandCounter)
	}{
		{
			name:    "github REPO_SLUG",
			doc:     map[string]string{"REPO_SLUG": ""},
			ambient: map[string]string{"REPO_SLUG": "o/r"},
			check: func(t *testing.T, c forge.DemandCounter) {
				// execClient is unexported; its dump names the type and repo.
				if got := fmt.Sprintf("%T %+v", c, c); !strings.Contains(got, "github.execClient") || !strings.Contains(got, "repo:o/r") {
					t.Errorf("source = %s, want a github client for o/r", got)
				}
			},
		},
		{
			name:    "forgejo REPO_SLUG and FORGEJO_BASE_URL",
			doc:     map[string]string{"ISSUE_TRACKER": "forgejo", "REPO_SLUG": "", "FORGEJO_BASE_URL": ""},
			ambient: map[string]string{"REPO_SLUG": "o/r", "FORGEJO_BASE_URL": "http://forgejo.example", "FORGEJO_TOKEN": "tok"},
		},
		{
			name: "jira JIRA_BASE_URL",
			doc: map[string]string{
				"ISSUE_TRACKER": "jira", "JIRA_BASE_URL": "", "JIRA_PROJECT_KEY": "PROJ",
				"JIRA_STATUS_MAPPING": `{"dispatchable":"To Do"}`,
			},
			ambient: map[string]string{"JIRA_BASE_URL": jiraSrv.URL, "JIRA_TOKEN": "tok"},
		},
		{
			name:    "local LOCAL_ISSUES_DIR",
			doc:     map[string]string{"ISSUE_TRACKER": "local", "LOCAL_ISSUES_DIR": ""},
			ambient: map[string]string{"LOCAL_ISSUES_DIR": localDir},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearKnobEnvT(t)
			for k, v := range tt.ambient {
				t.Setenv(k, v)
			}
			tt.doc["LABEL"] = "ready-for-agent"
			src, _ := buildDemandSources(demandDocT(tt.doc), allDemandKinds)
			if len(src) != 2 {
				t.Errorf("sources = %v, want work and research", src)
			}
			if tt.check != nil {
				for _, c := range src {
					tt.check(t, c)
				}
			}
		})
	}
}

// A probed work kind whose repo slug is supplied at runtime (an empty document
// value over an ambient one) is drawn by the loop: a child starts once Demand
// rises from 0 mid-run.
func TestLoop_ProbedWorkKindWithRuntimeSlugStartsWhenDemandRises(t *testing.T) {
	clearKnobEnvT(t)
	var probes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/o/r/issues" {
			http.NotFound(w, r)
			return
		}
		total := "0"
		if probes.Add(1) > 2 {
			total = "1"
		}
		w.Header().Set("X-Total-Count", total)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	t.Setenv("REPO_SLUG", "o/r")
	t.Setenv("FORGEJO_TOKEN", "tok")

	work := daemon.KindOf(dispatchkind.Work)
	src, _ := buildDemandSources(demandDocT(map[string]string{
		"ISSUE_TRACKER": "forgejo", "REPO_SLUG": "", "FORGEJO_BASE_URL": srv.URL, "LABEL": "ready-for-agent",
	}), []daemon.Kind{work})
	if _, ok := src[work]; !ok {
		t.Fatal("no demand source for work")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r := &demandLoopRunner{hostRunner: mustHostRunner(t, hostRunnerConfig{env: os.Environ(), demand: src}), cancel: cancel}
	cfg := daemon.Config{
		Kinds:            []daemon.Kind{work},
		ProbeIntervals:   probeIntervals(src, time.Millisecond),
		IdleFloor:        time.Millisecond,
		IdleCap:          time.Hour,
		FailureBackoff:   time.Millisecond,
		BreakerThreshold: 1000,
		BreakerWindow:    time.Minute,
		Slots:            1,
	}
	var out bytes.Buffer
	em := daemon.NewEmitter(&out, &out, time.Now)
	daemon.Loop(ctx, cfg, r, em, hostClock{})

	if r.started.Load() != 1 {
		t.Fatalf("children started = %d, want 1 (after %d probes)", r.started.Load(), probes.Load())
	}
	if probes.Load() < 3 {
		t.Errorf("probes = %d, want the child to start only once demand rose", probes.Load())
	}
}

// demandLoopRunner answers Demand from the real host runner's sources and
// stands in for git and nix: a fixed tip, and a child that stops the loop.
type demandLoopRunner struct {
	*hostRunner
	cancel  context.CancelFunc
	started atomic.Int32
}

func (r *demandLoopRunner) ResolveTip(context.Context) (daemon.Tip, error) {
	return daemon.Tip{Revision: "rev1"}, nil
}

func (r *demandLoopRunner) RunChild(context.Context, daemon.ChildRequest) (daemon.ChildResult, error) {
	r.started.Add(1)
	r.cancel()
	return daemon.ChildResult{}, nil
}

func TestBuildDemandSources_MissingReasonPerProbedKind(t *testing.T) {
	work, research, butler := daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research), daemon.KindOf(dispatchkind.Butler)
	for _, tc := range []struct {
		name     string
		settings map[string]string
		// wantWork/wantResearch are substrings of the reason; "" means the kind
		// builds a source and has no entry.
		wantWork, wantResearch string
	}{
		{"github without slug", map[string]string{"ISSUE_TRACKER": "github", "LABEL": "ready-for-agent"}, "REPO_SLUG", "REPO_SLUG"},
		{"forgejo without token", map[string]string{"ISSUE_TRACKER": "forgejo", "REPO_SLUG": "o/r", "FORGEJO_BASE_URL": "http://x", "LABEL": "ready-for-agent"}, "FORGEJO_TOKEN", "FORGEJO_TOKEN"},
		{"unknown tracker", map[string]string{"ISSUE_TRACKER": "bogus", "LABEL": "ready-for-agent"}, `unknown ISSUE_TRACKER "bogus"`, `unknown ISSUE_TRACKER "bogus"`},
		{"fully configured", map[string]string{"ISSUE_TRACKER": "local", "LOCAL_ISSUES_DIR": t.TempDir(), "LABEL": "ready-for-agent"}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearKnobEnvT(t)
			_, missing := buildDemandSources(demandDocT(tc.settings), allDemandKinds)
			if _, ok := missing[butler]; ok {
				t.Errorf("butler (never probed) has a missing entry: %v", missing)
			}
			for kind, want := range map[daemon.Kind]string{work: tc.wantWork, research: tc.wantResearch} {
				got, ok := missing[kind]
				if want == "" {
					if ok {
						t.Errorf("%s has missing entry %q, want none", kind, got)
					}
					continue
				}
				if !ok || !strings.Contains(got, want) {
					t.Errorf("%s reason = %q (present %v), want it to contain %q", kind, got, ok, want)
				}
			}
		})
	}
}

func TestBuildDemandSources_NoProbedKindHasNoMissingEntry(t *testing.T) {
	clearKnobEnvT(t)
	_, missing := buildDemandSources(demandDocT(map[string]string{"ISSUE_TRACKER": "bogus"}), []daemon.Kind{daemon.KindOf(dispatchkind.Butler)})
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none for a butler-only daemon", missing)
	}
}

func TestWarnUnprobedKinds_OneStderrLineAndEventPerKindNoSecret(t *testing.T) {
	clearKnobEnvT(t)
	const secret = "s3cr3t-token-value"
	_, missing := buildDemandSources(demandDocT(map[string]string{
		"ISSUE_TRACKER": "jira", "JIRA_PROJECT_KEY": "P", "JIRA_TOKEN": secret, "LABEL": "ready-for-agent",
	}), allDemandKinds)

	var stdout, stderr bytes.Buffer
	em := daemon.NewEmitter(&stdout, &stderr, time.Now)
	warnUnprobedKinds(missing, &stderr, em)

	lines := strings.Split(strings.TrimSuffix(stderr.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stderr lines = %d, want 2 (dispatch, research):\n%s", len(lines), stderr.String())
	}
	work, research := daemon.KindOf(dispatchkind.Work), daemon.KindOf(dispatchkind.Research)
	for i, kind := range []daemon.Kind{work, research} {
		for _, want := range []string{"kind " + string(kind) + " has no Demand source: ", "JIRA_BASE_URL", "child exits"} {
			if !strings.Contains(lines[i], want) {
				t.Errorf("line %d = %q, want it to contain %q", i, lines[i], want)
			}
		}
	}
	events := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2:\n%s", len(events), stdout.String())
	}
	for i, kind := range []daemon.Kind{work, research} {
		var ev daemon.Event
		if err := json.Unmarshal([]byte(events[i]), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Event != "demand_source_missing" || ev.Kind != kind || !strings.Contains(ev.Reason, "JIRA_BASE_URL") {
			t.Errorf("event %d = %+v, want demand_source_missing for %s naming JIRA_BASE_URL", i, ev, kind)
		}
	}
	if strings.Contains(stdout.String()+stderr.String(), secret) {
		t.Error("token value leaked into the startup output")
	}
}

func TestWarnUnprobedKinds_NothingMissingEmitsNothing(t *testing.T) {
	var stdout, stderr bytes.Buffer
	warnUnprobedKinds(nil, &stderr, daemon.NewEmitter(&stdout, &stderr, time.Now))
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Errorf("output = %q / %q, want none", stdout.String(), stderr.String())
	}
}

// sharedKnobAccessors maps each knob the daemon resolves as a child does to
// its field, so TestResolveChildSharedKnobs can read the value the daemon
// resolved for exactly that key.
var sharedKnobAccessors = map[string]func(childSharedKnobs) string{
	"BASE_BRANCH":          func(k childSharedKnobs) string { return k.baseBranch },
	"MAX_PARALLEL":         func(k childSharedKnobs) string { return k.maxParallel },
	"DAEMON_AWAKE_WINDOW":  func(k childSharedKnobs) string { return k.awakeWindow },
	"BUTLER_CHORES":        func(k childSharedKnobs) string { return k.butlerChores },
	"BUTLER_EVERY":         func(k childSharedKnobs) string { return k.butlerEvery },
	"BUTLER_CHORE_CLASSES": func(k childSharedKnobs) string { return k.butlerChoreClasses },
}

// TestResolveChildSharedKnobs pins issue #4623 for every knob a child also
// reads: the daemon resolves the value the child gets (document first, then
// ambient, then the schema default). Every key carries a distinct value, so a
// key resolved from another key's slot fails the case.
func TestResolveChildSharedKnobs(t *testing.T) {
	const wantErr = "no value for %s (not in environment or --input document settings)"
	required := map[string]bool{"BASE_BRANCH": true, "MAX_PARALLEL": true}
	type knobCase struct {
		name         string
		doc, ambient string
		docSet       bool
		want         string
		wantErr      string
	}
	for key, get := range sharedKnobAccessors {
		cases := []knobCase{
			{name: "document wins over a differing ambient", docSet: true, doc: "doc-" + key, ambient: "amb-" + key, want: "doc-" + key},
			{name: "empty document value falls to ambient", docSet: true, ambient: "amb-" + key, want: "amb-" + key},
			{name: "absent from document falls to ambient", ambient: "amb-" + key, want: "amb-" + key},
			{name: "document and ambient agree", docSet: true, doc: "same-" + key, ambient: "same-" + key, want: "same-" + key},
		}
		if required[key] {
			cases = append(cases, knobCase{name: "neither is a config error", wantErr: fmt.Sprintf(wantErr, key)})
		} else {
			cases = append(cases, knobCase{name: "neither is the schema default", want: inputdoc.SchemaDefault(key)})
		}
		for _, tt := range cases {
			t.Run(key+"/"+tt.name, func(t *testing.T) {
				clearKnobEnvT(t)
				settings := map[string]string{}
				for other := range sharedKnobAccessors {
					t.Setenv(other, "other-"+other)
				}
				t.Setenv(key, tt.ambient)
				if tt.docSet {
					settings[key] = tt.doc
				}
				got, err := resolveChildSharedKnobs(&inputdoc.Document{Settings: settings})
				if tt.wantErr != "" {
					if err == nil || err.Error() != tt.wantErr {
						t.Fatalf("err = %v, want %q", err, tt.wantErr)
					}
					return
				}
				if err != nil {
					t.Fatalf("resolveChildSharedKnobs() err = %v", err)
				}
				if v := get(got); v != tt.want {
					t.Errorf("%s = %q, want %q", key, v, tt.want)
				}
			})
		}
	}
}

// TestResolveChildSharedKnobs_ButlerEveryDefault pins the one default value
// the table above only checks against SchemaDefault itself.
func TestResolveChildSharedKnobs_ButlerEveryDefault(t *testing.T) {
	clearKnobEnvT(t)
	got, err := resolveChildSharedKnobs(&inputdoc.Document{Settings: map[string]string{"BASE_BRANCH": "main", "MAX_PARALLEL": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.butlerEvery != "6h" {
		t.Errorf("butlerEvery = %q, want the schema default 6h", got.butlerEvery)
	}
}

// TestResolveDaemonOnlyKnobs_AmbientWins pins ADR 0020 for the knobs no child
// reads: a non-empty ambient value beats a differing document value. It walks
// daemonOnlyKnobs, so a key moved off Lookup (and out of the set) or added to
// it must change this test too.
func TestResolveDaemonOnlyKnobs_AmbientWins(t *testing.T) {
	accessors := map[string]func(daemonOnlyRaw) string{
		"DAEMON_APP":               func(k daemonOnlyRaw) string { return k.app },
		"DAEMON_SELF_APP":          func(k daemonOnlyRaw) string { return k.selfApp },
		"DAEMON_IDLE_FLOOR":        func(k daemonOnlyRaw) string { return k.idleFloor },
		"DAEMON_IDLE_CAP":          func(k daemonOnlyRaw) string { return k.idleCap },
		"DAEMON_PROBE_INTERVAL":    func(k daemonOnlyRaw) string { return k.probeInterval },
		"DAEMON_FAILURE_BACKOFF":   func(k daemonOnlyRaw) string { return k.failureBackoff },
		"DAEMON_BREAKER_THRESHOLD": func(k daemonOnlyRaw) string { return k.breakerThreshold },
		"DAEMON_BREAKER_WINDOW":    func(k daemonOnlyRaw) string { return k.breakerWindow },
		"RESEARCH_RESERVATION":     func(k daemonOnlyRaw) string { return k.researchReservation },
	}
	for key := range daemonOnlyKnobs {
		if accessors[key] == nil {
			t.Errorf("daemonOnlyKnobs lists %s but this test has no accessor for it", key)
		}
	}
	for key := range accessors {
		if !daemonOnlyKnobs[key] {
			t.Errorf("test reads %s, which is not in daemonOnlyKnobs", key)
		}
	}
	clearKnobEnvT(t)
	settings := map[string]string{}
	for key := range daemonOnlyKnobs {
		settings[key] = "doc-" + key
		t.Setenv(key, "amb-"+key)
	}
	const withSelf, multiKind = true, true
	got, err := resolveDaemonOnlyKnobs(&inputdoc.Document{Settings: settings}, io.Discard, withSelf, multiKind)
	if err != nil {
		t.Fatal(err)
	}
	for key := range daemonOnlyKnobs {
		if want, v := "amb-"+key, accessors[key](got); v != want {
			t.Errorf("%s = %q, want ambient %q over the document value", key, v, want)
		}
	}
}
