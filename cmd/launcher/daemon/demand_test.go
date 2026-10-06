package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
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
	src := buildDemandSources(doc, allDemandKinds)

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
	src := buildDemandSources(demandDocT(map[string]string{
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
		src := buildDemandSources(demandDocT(map[string]string{
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
			src := buildDemandSources(demandDocT(map[string]string{
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
	src := buildDemandSources(demandDocT(map[string]string{
		"ISSUE_TRACKER": "github", "LABEL": "ready-for-agent",
	}), allDemandKinds)
	if len(src) != 2 {
		t.Errorf("sources = %v, want work and research", src)
	}
}

func TestBuildDemandSources_NoAdapterOrMissingKnobsHasNoSource(t *testing.T) {
	clearKnobEnvT(t)
	for name, settings := range map[string]map[string]string{
		"github without slug": {"ISSUE_TRACKER": "github", "LABEL": "ready-for-agent"},
		"jira":                {"ISSUE_TRACKER": "jira", "REPO_SLUG": "o/r", "LABEL": "ready-for-agent"},
		"forgejo":             {"ISSUE_TRACKER": "forgejo", "REPO_SLUG": "o/r", "LABEL": "ready-for-agent"},
		"absent":              {},
	} {
		t.Run(name, func(t *testing.T) {
			src := buildDemandSources(demandDocT(settings), allDemandKinds)
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

	src := buildDemandSources(demandDocT(map[string]string{
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

	src := buildDemandSources(demandDocT(map[string]string{
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
	src := buildDemandSources(demandDocT(map[string]string{
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
	src := buildDemandSources(demandDocT(map[string]string{
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

// A blank LABEL leaves work with nothing to count, so it stays exit-driven;
// research's family is fixed and unaffected.
func TestBuildDemandSources_BlankWorkLabelLeavesWorkExitDriven(t *testing.T) {
	clearKnobEnvT(t)
	src := buildDemandSources(demandDocT(map[string]string{
		"ISSUE_TRACKER": "local", "LOCAL_ISSUES_DIR": t.TempDir(),
	}), allDemandKinds)
	if _, ok := src[daemon.KindOf(dispatchkind.Work)]; ok {
		t.Error("work has a source despite a blank LABEL")
	}
	if _, ok := src[daemon.KindOf(dispatchkind.Research)]; !ok {
		t.Error("research lost its source")
	}
}

func TestBuildDemandSources_ForgejoPerKindSourceWithProbeInterval(t *testing.T) {
	clearKnobEnvT(t)
	src := buildDemandSources(demandDocT(map[string]string{
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
	src := buildDemandSources(demandDocT(map[string]string{
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
			if src := buildDemandSources(demandDocT(settings), allDemandKinds); len(src) != 0 {
				t.Errorf("sources = %v, want none (kind stays exit-driven)", src)
			}
		})
	}
}
