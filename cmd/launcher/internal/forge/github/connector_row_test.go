package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorrecord"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/hostpaths"
)

// routedReads is every read a routed row routes, each driven through the
// production execClient.
var routedReads = []struct {
	name string
	read func(c *execClient, prURL string) error
}{
	{"ListIssues", func(c *execClient, _ string) error { _, err := c.ListIssues(forge.Dispatchable); return err }},
	{"ListOpenIssues", func(c *execClient, _ string) error { _, err := c.ListOpenIssues(); return err }},
	{"Issue", func(c *execClient, _ string) error { _, err := c.Issue("1"); return err }},
	{"OpenPRForBranch", func(c *execClient, _ string) error { _, _, err := c.OpenPRForBranch("agent/issue-1"); return err }},
	{"PRForBranch", func(c *execClient, _ string) error { _, _, err := c.PRForBranch("agent/issue-1"); return err }},
	{"PRState", func(c *execClient, u string) error { _, err := c.PRState(u); return err }},
	{"CheckState", func(c *execClient, u string) error { _, err := c.CheckState(u); return err }},
	{"FailureDetail", func(c *execClient, u string) error { _, err := c.FailureDetail(u); return err }},
	{"CheckRun", func(c *execClient, u string) error { _, _, err := c.CheckRun(u); return err }},
	{"Comments", func(c *execClient, _ string) error { _, err := c.Comments("1"); return err }},
	{"NativeDepsOf", func(c *execClient, _ string) error { _, err := c.NativeDepsOf("1"); return err }},
	{"BlocksOf", func(c *execClient, _ string) error { _, err := c.BlocksOf("1"); return err }},
	{"SearchIssues", func(c *execClient, _ string) error {
		_, err := c.SearchIssues("one", forge.IssueOpen, "", 5)
		return err
	}},
	{"ListIssuesWithLabels", func(c *execClient, _ string) error {
		_, err := c.ListIssuesWithLabels(forge.IssueClosed, []string{"agent-review-finding"})
		return err
	}},
	{"BranchExists", func(c *execClient, _ string) error { _, err := c.BranchExists("agent/issue-1"); return err }},
	{"MergeCommit", func(c *execClient, u string) error { _, err := c.MergeCommit(u); return err }},
	{"HeadCommitSHA", func(c *execClient, u string) error { _, err := c.HeadCommitSHA(u); return err }},
	{"Mergeable", func(c *execClient, u string) error { _, err := c.Mergeable(u); return err }},
	{"NeedsUpdate", func(c *execClient, u string) error { _, err := c.NeedsUpdate(u); return err }},
	{"ListPRFiles", func(c *execClient, u string) error { _, err := c.ListPRFiles(u); return err }},
}

// routedLogHarness is harnessWithRow over a routed row, run in a scratch
// directory so the Connector record files land there. GH_HOST names the scripted
// backend, as it names the web host on a real host.
func routedLogHarness(t *testing.T, useRow func(*testing.T, string)) (h *connectorHarness, c *execClient, calls func() int, logDir string) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	h, c, calls = harnessWithRow(t, useRow)
	t.Setenv("GH_HOST", h.backend.host)
	return h, c, calls, hostpaths.LogDir(dir)
}

func connectorFiles(t *testing.T, logDir, suffix string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(logDir, "connector-github-*"+suffix))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Under a routed row every read the Connector declares is answered by the
// Connector over the API, never by gh.
func TestConnectorRow_ReadsGoThroughTheConnector(t *testing.T) {
	h, c, calls, logDir := routedLogHarness(t, withGithubRowAt)
	h.SeedIssue(forge.Issue{Number: "1", Title: "one", Labels: []string{testLabels.Dispatchable}})
	h.SeedIssue(forge.Issue{Number: "2", Title: "two"})
	prURL := h.SeedOpenPR("1")
	h.mutatePR("1", func(p *restPR) { p.branch = "agent/issue-1"; p.files = []string{"README.md"} })
	h.SeedCheckStates(prURL, []forge.RollupState{forge.StateFailure})
	h.SeedFailingCheck(prURL, "unit", "FAILURE", "boom")
	h.SeedNativeDeps("1", []string{"2"})
	h.SeedBlocks("1", []string{"2"})

	if got, err := c.ListIssues(forge.Dispatchable); err != nil || len(got) != 1 || got[0].Number != "1" {
		t.Errorf("ListIssues = %v, %v; want issue 1", got, err)
	}
	if got, err := c.ListOpenIssues(); err != nil || len(got) != 2 {
		t.Errorf("ListOpenIssues = %v, %v; want 2 issues", got, err)
	}
	if got, err := c.Issue("2"); err != nil || got.Title != "two" {
		t.Errorf("Issue(2) = %+v, %v; want title two", got, err)
	}
	if pr, ok, err := c.OpenPRForBranch("agent/issue-1"); err != nil || !ok || pr.URL != prURL {
		t.Errorf("OpenPRForBranch = %+v, %v, %v; want %s", pr, ok, err, prURL)
	}
	if u, ok, err := c.PRForBranch("agent/issue-1"); err != nil || !ok || u != prURL {
		t.Errorf("PRForBranch = %q, %v, %v; want %s", u, ok, err, prURL)
	}
	if st, err := c.PRState(prURL); err != nil || st != forge.PROpen {
		t.Errorf("PRState = %q, %v; want open", st, err)
	}
	if st, err := c.CheckState(prURL); err != nil || st != forge.StateFailure {
		t.Errorf("CheckState = %q, %v; want failure", st, err)
	}
	if d, err := c.FailureDetail(prURL); err != nil || !strings.Contains(d, "unit") {
		t.Errorf("FailureDetail = %q, %v; want the failing check named", d, err)
	}
	if got, err := c.NativeDepsOf("1"); err != nil || len(got) != 1 || got[0] != "2" {
		t.Errorf("NativeDepsOf = %v, %v; want [2]", got, err)
	}
	if got, err := c.BlocksOf("1"); err != nil || len(got) != 1 || got[0].ID != "2" {
		t.Errorf("BlocksOf = %v, %v; want [2]", got, err)
	}
	if got, err := c.SearchIssues("two", forge.IssueOpen, "", 5); err != nil || len(got) != 1 || got[0].Number != "2" {
		t.Errorf("SearchIssues = %v, %v; want issue 2", got, err)
	}
	h.SeedClosed("3", "three", []string{"agent-review-finding"}, "2026-02-03T04:05:06Z", true)
	if got, err := c.ListIssuesWithLabels(forge.IssueClosed, []string{"agent-review-finding"}); err != nil || len(got) != 1 || got[0].Number != "3" || !got[0].ClosedNotPlanned || got[0].ClosedAt.IsZero() {
		t.Errorf("ListIssuesWithLabels = %+v, %v; want closed not-planned issue 3 with its close date", got, err)
	}
	if _, err := c.BranchExists("agent/issue-1"); err != nil {
		t.Errorf("BranchExists: %v", err)
	}
	if sha, err := c.HeadCommitSHA(prURL); err != nil || sha == "" {
		t.Errorf("HeadCommitSHA = %q, %v; want a SHA", sha, err)
	}
	if _, err := c.MergeCommit(prURL); err != nil {
		t.Errorf("MergeCommit: %v", err)
	}
	if st, err := c.Mergeable(prURL); err != nil || st == "" {
		t.Errorf("Mergeable = %q, %v; want a state", st, err)
	}
	if stale, err := c.NeedsUpdate(prURL); err != nil || stale {
		t.Errorf("NeedsUpdate = %v, %v; want false", stale, err)
	}
	if got, err := c.ListPRFiles(prURL); err != nil || len(got) != 1 {
		t.Errorf("ListPRFiles = %v, %v; want [README.md]", got, err)
	}

	if n := calls(); n != 0 {
		t.Errorf("the Connector reads ran gh %d times", n)
	}
	if logs := connectorFiles(t, logDir, ".log"); len(logs) != 0 {
		t.Errorf("clean reads wrote Connector error logs %v", logs)
	}
	if len(connectorFiles(t, logDir, ".audit.jsonl")) != 1 {
		t.Errorf("want one Connector audit file, got %v", connectorFiles(t, logDir, ".audit.jsonl"))
	}
}

// A Connector row runs gh for none of its reads.
func TestConnectorRow_NoReadRunsGh(t *testing.T) {
	for _, tc := range routedReads {
		t.Run(tc.name, func(t *testing.T) {
			_, c, calls, _ := routedLogHarness(t, withGithubRowAt)

			_ = tc.read(c, "https://github.com/owner/repo/pull/1")

			if n := calls(); n != 0 {
				t.Errorf("%s under a Connector row ran gh %d times", tc.name, n)
			}
		})
	}
}

// A Connector row's Connector audits to the run's Connector record.
func TestConnectorRow_AuditsToTheConnectorRecord(t *testing.T) {
	_, c, _, logDir := routedLogHarness(t, withGithubRowAt)

	if _, err := c.ListIssues(forge.Dispatchable); err != nil {
		t.Fatal(err)
	}

	if len(connectorFiles(t, logDir, ".audit.jsonl")) != 1 {
		t.Errorf("want one Connector audit file, got %v", connectorFiles(t, logDir, ".audit.jsonl"))
	}
}

// A Connector that fails to load fails each routed read with the load error,
// never reaching gh, and each failure lands in the run's Connector log.
func TestConnectorRow_LoadFailureFailsTheReadAndIsRecorded(t *testing.T) {
	for _, tc := range routedReads {
		t.Run(tc.name, func(t *testing.T) {
			_, c, calls, logDir := routedLogHarness(t, withGithubRowAt)
			boom := errors.New("github connector failed to load")
			hookLoads(t, func(func() (*connectorhost.Connector, error)) (*connectorhost.Connector, error) { return nil, boom })

			err := tc.read(c, "https://"+Host()+"/owner/repo/pull/1")

			if !errors.Is(err, boom) {
				t.Errorf("err = %v, want the load error", err)
			}
			if n := calls(); n != 0 {
				t.Errorf("the failed read ran gh %d times", n)
			}
			logs := connectorFiles(t, logDir, ".log")
			if len(logs) != 1 {
				t.Fatalf("Connector logs = %v, want one", logs)
			}
			raw, err := os.ReadFile(logs[0])
			if err != nil {
				t.Fatal(err)
			}
			var rec struct{ Op, Error string }
			if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &rec); err != nil {
				t.Fatalf("Connector log %q: %v", raw, err)
			}
			if rec.Op == "" || !strings.Contains(rec.Error, boom.Error()) {
				t.Errorf("record = %+v, want an op naming the load error", rec)
			}
		})
	}
}

// A request outside the row's Routes is refused by the host: the read fails
// with that refusal, never falls back to gh, and the Connector audit shows it.
func TestConnectorRow_RefusedRouteFailsTheReadAndIsAudited(t *testing.T) {
	_, c, calls, logDir := routedLogHarness(t, func(t *testing.T, host string) {
		decodeGithubRow(t, host, func(r *backendrows.Row) {
			r.Routes = r.Routes[:0:0]
			for _, rt := range allRoutes(t, host) {
				if !strings.Contains(rt.Path, "/issues") {
					r.Routes = append(r.Routes, rt)
				}
			}
		})
	})

	_, err := c.ListOpenIssues()

	if !errors.Is(err, connectorhost.ErrRouteRefused) {
		t.Fatalf("ListOpenIssues over a row without the issues Route = %v, want the route refusal", err)
	}
	if n := calls(); n != 0 {
		t.Errorf("the refused read ran gh %d times", n)
	}
	audits := connectorFiles(t, logDir, ".audit.jsonl")
	if len(audits) != 1 {
		t.Fatalf("Connector audit files = %v, want one", audits)
	}
	raw, rerr := os.ReadFile(audits[0])
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(string(raw), `"decision":"refused"`) {
		t.Errorf("Connector audit %q has no refused entry", raw)
	}
	logs := connectorFiles(t, logDir, ".log")
	if len(logs) != 1 {
		t.Fatalf("Connector logs = %v, want one", logs)
	}
	if rec, _ := os.ReadFile(logs[0]); !strings.Contains(string(rec), `"op":"list open issues"`) {
		t.Errorf("Connector log %q lacks the failed op", rec)
	}
}

// A read that kills the shared Connector still fails, and is recorded, but the
// next read loads a fresh one instead of failing with "connector unusable".
func TestConnectorRow_FatalCallFailureReloadsTheConnectorForTheNextRead(t *testing.T) {
	_, c, calls, logDir := routedLogHarness(t, func(t *testing.T, host string) {
		decodeGithubRow(t, host, func(r *backendrows.Row) {
			r.Routes = r.Routes[:0:0]
			for _, rt := range allRoutes(t, host) {
				if !strings.Contains(rt.Path, "/issues") {
					r.Routes = append(r.Routes, rt)
				}
			}
		})
	})
	loads := countLoads(t)

	if _, err := c.ListOpenIssues(); !errors.Is(err, connectorhost.ErrRouteRefused) {
		t.Fatalf("ListOpenIssues = %v, want the route refusal", err)
	}
	if _, err := c.ListLabels(); err != nil {
		t.Fatalf("the read after the fatal failure = %v, want it served by a fresh Connector", err)
	}

	if *loads != 2 {
		t.Errorf("Connector loaded %d times, want 2 (the first, then one reload)", *loads)
	}
	if n := calls(); n != 0 {
		t.Errorf("the reads ran gh %d times", n)
	}
	audits := connectorFiles(t, logDir, ".audit.jsonl")
	if len(audits) != 1 {
		t.Fatalf("Connector audit files = %v, want one", audits)
	}
	if raw, _ := os.ReadFile(audits[0]); !strings.Contains(string(raw), `"decision":"refused"`) {
		t.Errorf("Connector audit %q lost the refused entry", raw)
	}
	logs := connectorFiles(t, logDir, ".log")
	if len(logs) != 1 {
		t.Fatalf("Connector logs = %v, want one", logs)
	}
	rec, _ := os.ReadFile(logs[0])
	for _, want := range []string{`"op":"list open issues"`, `"op":"reload the Connector"`} {
		if !strings.Contains(string(rec), want) {
			t.Errorf("Connector log %q lacks %s", rec, want)
		}
	}
}

func allRoutes(t *testing.T, host string) []connectorhost.Route {
	t.Helper()
	_, routes := githubConnectorRoutes(t, host, connectorRepo)
	return routes
}

// A Connector lacking the capability a routed read needs fails it as
// unsupported, never falling back to gh.
func TestConnectorRow_MissingCapabilityFailsTheReadWithoutGh(t *testing.T) {
	_, c, calls, _ := routedLogHarness(t, withGithubRowAt)
	withoutCapability(t, "tracker.nativeDeps")

	_, err := c.NativeDepsOf("1")

	if !errors.Is(err, backendkit.ErrUnsupported) {
		t.Errorf("NativeDepsOf = %v, want unsupported", err)
	}
	if n := calls(); n != 0 {
		t.Errorf("the unsupported read ran gh %d times", n)
	}
}

// Every read a row routes records its failure: a
// Connector that fails to load lands a line in the Connector log for each.
func TestConnectorRow_EveryReadRecordsItsFailure(t *testing.T) {
	for name, read := range map[string]func(c *execClient) error{
		"CountReady":      func(c *execClient) error { _, err := c.CountReady(true); return err },
		"Probe":           func(c *execClient) error { _, err := c.Probe(); return err },
		"ListLabels":      func(c *execClient) error { _, err := c.ListLabels(); return err },
		"BranchProtected": func(c *execClient) error { _, err := c.BranchProtected("main"); return err },
		"IntrospectToken": func(c *execClient) error {
			_, err := introspectToken(c.connectors, "ghp_abc", connectorRepo)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, c, _, logDir := routedLogHarness(t, withGithubRowAt)
			boom := errors.New("github connector failed to load")
			hookLoads(t, func(func() (*connectorhost.Connector, error)) (*connectorhost.Connector, error) { return nil, boom })

			if err := read(c); !errors.Is(err, boom) {
				t.Fatalf("err = %v, want the load error", err)
			}

			logs := connectorFiles(t, logDir, ".log")
			if len(logs) != 1 {
				t.Fatalf("Connector logs = %v, want one", logs)
			}
			if raw, _ := os.ReadFile(logs[0]); !strings.Contains(string(raw), boom.Error()) {
				t.Errorf("Connector log %q lacks the load error", raw)
			}
		})
	}
}

// A request the host admitted but could not send surfaces its host-side reason
// in the read's error and the Connector log, naming the credential variable and
// never its value.
func TestConnectorRow_UnsetCredentialIsNamedInTheReadErrorAndRecord(t *testing.T) {
	_, c, _, logDir := routedLogHarness(t, withGithubRowAt)
	t.Setenv(tokenCredentialEnv, "")

	_, err := c.Issue("1")
	want := tokenCredentialEnv + " is not set"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want it to name %q", err, want)
	}

	logs := connectorFiles(t, logDir, ".log")
	if len(logs) != 1 {
		t.Fatalf("Connector logs = %v, want one", logs)
	}
	raw, _ := os.ReadFile(logs[0])
	if !strings.Contains(string(raw), want) {
		t.Errorf("Connector log %q lacks %q", raw, want)
	}
	if strings.Contains(err.Error()+string(raw), connectorToken) {
		t.Errorf("the credential value leaked into the error or log: %v / %q", err, raw)
	}
}

// Under a routed row token introspection audits to the Connector record.
func TestConnectorRow_TokenIntrospectionAuditsToTheConnectorRecord(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	b := newTokenBackend(t, "ghp_abc")
	withGithubRowAt(t, b.Listener.Addr().String())
	old := connectorOptions
	connectorOptions = backendrows.Options{Client: b.Client(), PublicForTest: connectorhost.LoopbackForTest}
	t.Cleanup(func() { connectorOptions = old })
	t.Setenv(tokenCredentialEnv, connectorToken)

	if _, err := introspectToken(testRuntime(t), "ghp_abc", connectorRepo); err != nil {
		t.Fatal(err)
	}

	logDir := hostpaths.LogDir(dir)
	if len(connectorFiles(t, logDir, ".audit.jsonl")) != 1 {
		t.Errorf("Connector audit files = %v, want one", connectorFiles(t, logDir, ".audit.jsonl"))
	}
}

// The merge-commit read is optional on a Code Forge: a view without it is an
// unsupported read, not a nil dereference.
func TestMergeCommitFrom_WithoutTheReaderIsMissingCapability(t *testing.T) {
	_, err := mergeCommitFrom(forge.Capabilities{}, "https://github.com/owner/repo/pull/1")

	if !errors.Is(err, errNoCapability) {
		t.Errorf("mergeCommitFrom without a reader = %v, want errNoCapability", err)
	}
}

// CheckRun is the merge wait's CI poll: under a routed row it reads the state
// and the in-flight run link from the Connector, never gh.
func TestConnectorRow_CheckRunComesFromTheConnector(t *testing.T) {
	h, c, calls, _ := routedLogHarness(t, withGithubRowAt)
	prURL := h.SeedOpenPR("1")
	h.SeedCheckStates(prURL, []forge.RollupState{forge.StatePending})
	runs := h.backend.web + "/actions/runs/"
	h.mutatePR("1", func(p *restPR) {
		p.contexts = append(p.contexts,
			scriptedContext{TypeName: "CheckRun", Name: "done", Status: "COMPLETED", DetailsURL: runs + "1/job/10"},
			scriptedContext{TypeName: "CheckRun", Name: "live", Status: "IN_PROGRESS", DetailsURL: runs + "2/job/20"})
	})

	state, run, err := c.CheckRun(prURL)

	if err != nil || state != forge.StatePending || run != runs+"2" {
		t.Errorf("CheckRun = %q, %q, %v; want pending, %s", state, run, err, runs+"2")
	}
	if n := calls(); n != 0 {
		t.Errorf("CheckRun ran gh %d times", n)
	}
}

// A Connector without forge.ciRun is an unsupported read, not a silent gh poll.
func TestConnectorRow_CheckRunWithoutTheCapabilityFailsWithoutGh(t *testing.T) {
	withoutCapability(t, "forge.ciRun")
	h, c, calls, _ := routedLogHarness(t, withGithubRowAt)
	prURL := h.SeedOpenPR("1")

	_, _, err := c.CheckRun(prURL)

	if !errors.Is(err, backendkit.ErrUnsupported) {
		t.Errorf("CheckRun = %v, want ErrUnsupported", err)
	}
	if n := calls(); n != 0 {
		t.Errorf("CheckRun ran gh %d times", n)
	}
}

// routedSandboxHarness chdirs into a scratch directory, decodes the row via
// useRow, and returns the audit file path
// this process would write. A non-nil seed is written there first; nil leaves
// the file absent.
func routedSandboxHarness(t *testing.T, useRow func(*testing.T, string), seed []byte) (audit string) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	useRow(t, "example.test")
	audit = filepath.Join(hostpaths.LogDir(dir), connectorrecord.Name(backend.GitHub.Name, ".audit.jsonl"))
	if seed != nil {
		if err := os.MkdirAll(filepath.Dir(audit), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(audit, seed, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return audit
}

const seededAudit = "{\"decision\":\"allowed\"}\n"

var knownStats = connectorsandbox.Stats{
	Start:    7 * time.Millisecond,
	HopStats: connectorhost.HopStats{Calls: 5, HostCalls: 3, Hop: 11 * time.Millisecond},
}

func TestRecordRoutedSandboxTotals_AppendsOneLineToTheAuditFile(t *testing.T) {
	audit := routedSandboxHarness(t, withGithubRowAt, []byte(seededAudit))

	RecordRoutedSandboxTotals(testRuntime(t), knownStats)

	b, err := os.ReadFile(audit)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("audit file = %q, want the existing line and one totals line", b)
	}
	want := fmt.Sprintf(`{"sandbox_start_ns":%d,"sandbox_calls":5,"sandbox_host_calls":3,"sandbox_hop_ns":%d}`,
		int64(7*time.Millisecond), int64(11*time.Millisecond))
	if lines[1] != want {
		t.Errorf("totals line = %s, want %s", lines[1], want)
	}
	if got := connectorFiles(t, filepath.Dir(audit), ".log"); len(got) != 0 {
		t.Errorf("recording totals created %v, want no .log", got)
	}

	// The docs promise the keys shadow_summary carries.
	var keys map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &keys); err != nil {
		t.Fatal(err)
	}
	summary := map[string]bool{}
	st := reflect.TypeOf(hostop.ShadowSummary{})
	for i := range st.NumField() {
		name, _, _ := strings.Cut(st.Field(i).Tag.Get("json"), ",")
		summary[name] = true
	}
	for k := range keys {
		if !summary[k] {
			t.Errorf("totals key %q is not a hostop.ShadowSummary json key", k)
		}
	}
}

// A process that never read through its Connector has no record pair,
// and recording must not make one: a pair reads as a dirty run.
func TestRecordRoutedSandboxTotals_CreatesNoRecordPair(t *testing.T) {
	audit := routedSandboxHarness(t, withGithubRowAt, nil)

	RecordRoutedSandboxTotals(testRuntime(t), knownStats)

	if got := connectorFiles(t, filepath.Dir(audit), ""); len(got) != 0 {
		t.Errorf("recording totals created %v, want no files", got)
	}
}

func TestRecordRoutedSandboxTotals_AppendsUnderAPlainRow(t *testing.T) {
	audit := routedSandboxHarness(t, withGithubRowAt, []byte(seededAudit))

	RecordRoutedSandboxTotals(testRuntime(t), knownStats)

	if b, _ := os.ReadFile(audit); len(strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")) != 2 {
		t.Errorf("audit file = %q, want the existing line and one totals line", b)
	}
}

// The runtime decides whether the row is routed: one holding no github row
// writes nothing, even where an audit file already exists.
func TestRecordRoutedSandboxTotals_WritesNothingWithoutARowOnTheRuntime(t *testing.T) {
	audit := routedSandboxHarness(t, withGithubRowAt, []byte(seededAudit))

	RecordRoutedSandboxTotals(nil, knownStats)
	RecordRoutedSandboxTotals(connectorruntime.New(backendrows.Set{}, nil, testSandbox()), knownStats)

	if b, _ := os.ReadFile(audit); string(b) != seededAudit {
		t.Errorf("audit file = %q, want it untouched", b)
	}
}
