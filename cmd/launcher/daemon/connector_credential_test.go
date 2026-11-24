package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge/github"
	"spindrift.dev/launcher/internal/inputdoc"
)

// ghTokenEnv is the github row's one credential: core's own GH_TOKEN.
const ghTokenEnv = "GH_TOKEN"

// useGithubConnectorRow applies a github backend row.
func useGithubConnectorRow(t *testing.T) {
	t.Helper()
	t.Cleanup(backendrows.Snapshot())
	raw := `[{"name":"github","module":"/nonexistent.wasm","credentialEnv":"` + ghTokenEnv + `",
"routes":[{"method":"GET","host":"api.github.com","path":"/repos/{repo}/issues"}]}]`
	if err := backendrows.Use([]byte(raw), func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
}

func stubSecretCmd(t *testing.T, run func(string) (string, error)) {
	t.Helper()
	saved := secretCmdRunner
	secretCmdRunner = run
	t.Cleanup(func() { secretCmdRunner = saved })
}

func failIfRun(t *testing.T) func(string) (string, error) {
	return func(cmd string) (string, error) {
		t.Errorf("secret command ran: %q", cmd)
		return "", errors.New("must not run")
	}
}

var workKinds = []daemon.Kind{daemon.KindOf(dispatchkind.Work)}

func TestResolveConnectorCredentials_CmdFormFillsCredentialEnv(t *testing.T) {
	clearKnobEnvT(t)
	useGithubConnectorRow(t)
	t.Setenv(ghTokenEnv, "")
	t.Setenv(ghTokenEnv+"_CMD", "fetch-token")
	var ran string
	stubSecretCmd(t, func(cmd string) (string, error) { ran = cmd; return "tok-1\n", nil })

	if _, err := resolveConnectorCredentials(demandDocT(map[string]string{"ISSUE_TRACKER": "github"}), workKinds, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if ran != "fetch-token" {
		t.Errorf("ran %q, want fetch-token", ran)
	}
	if got := os.Getenv(ghTokenEnv); got != "tok-1" {
		t.Errorf("%s = %q, want tok-1", ghTokenEnv, got)
	}
}

func TestResolveConnectorCredentials_CmdWinsOverPlainEnv(t *testing.T) {
	clearKnobEnvT(t)
	useGithubConnectorRow(t)
	t.Setenv(ghTokenEnv, "stale")
	t.Setenv(ghTokenEnv+"_CMD", "fetch-token")
	stubSecretCmd(t, func(string) (string, error) { return "fresh\n", nil })
	refresh, err := resolveConnectorCredentials(demandDocT(map[string]string{"ISSUE_TRACKER": "github"}), workKinds, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if refresh == nil {
		t.Error("refresh is nil, want the _CMD credential refreshed")
	}
	if got := os.Getenv(ghTokenEnv); got != "fresh" {
		t.Errorf("%s = %q, want fresh", ghTokenEnv, got)
	}
}

func TestResolveConnectorCredentials_SecretCmdTemplateAloneResolves(t *testing.T) {
	clearKnobEnvT(t)
	useGithubConnectorRow(t)
	t.Setenv(ghTokenEnv, "")
	t.Setenv(ghTokenEnv+"_CMD", "")
	t.Setenv("SECRET_CMD", "rbw get spindrift-{name}")
	out := "tok-1"
	var ran string
	stubSecretCmd(t, func(cmd string) (string, error) { ran = cmd; return out, nil })
	doc := demandDocT(map[string]string{"ISSUE_TRACKER": "github"})

	refresh, err := resolveConnectorCredentials(doc, workKinds, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if ran != "rbw get spindrift-gh-token" {
		t.Errorf("ran %q, want the template rendered for the credential env", ran)
	}
	if got := os.Getenv(ghTokenEnv); got != "tok-1" {
		t.Errorf("%s = %q, want tok-1", ghTokenEnv, got)
	}
	if refresh == nil {
		t.Fatal("refresh is nil, want the template-sourced credential refreshed")
	}
	out = "tok-2"
	refresh()
	if got := os.Getenv(ghTokenEnv); got != "tok-2" {
		t.Errorf("after refresh %s = %q, want tok-2", ghTokenEnv, got)
	}

	// A restart-in-place successor inherits the Setenv'd value: it must
	// re-resolve rather than treat that value as an operator-set plain one.
	out = "tok-3"
	if _, err := resolveConnectorCredentials(doc, workKinds, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(ghTokenEnv); got != "tok-3" {
		t.Errorf("successor %s = %q, want tok-3", ghTokenEnv, got)
	}
}

func TestResolveConnectorCredentials_RefusesWhenNeitherFormSet(t *testing.T) {
	clearKnobEnvT(t)
	useGithubConnectorRow(t)
	t.Setenv(ghTokenEnv, "")
	t.Setenv(ghTokenEnv+"_CMD", "")
	stubSecretCmd(t, failIfRun(t))
	_, err := resolveConnectorCredentials(demandDocT(map[string]string{"ISSUE_TRACKER": "github"}), workKinds, &bytes.Buffer{})
	if err == nil {
		t.Fatal("want a refusal")
	}
	for _, want := range []string{ghTokenEnv, ghTokenEnv + "_CMD", "SECRET_CMD"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

func TestResolveConnectorCredentials_CmdFailureRefusesWithoutSecret(t *testing.T) {
	clearKnobEnvT(t)
	useGithubConnectorRow(t)
	t.Setenv(ghTokenEnv, "")
	t.Setenv(ghTokenEnv+"_CMD", "fetch-token")
	stubSecretCmd(t, func(string) (string, error) { return "partial-secret", errors.New("boom partial-secret") })
	_, err := resolveConnectorCredentials(demandDocT(map[string]string{"ISSUE_TRACKER": "github"}), workKinds, &bytes.Buffer{})
	if err == nil || strings.Contains(err.Error(), "partial-secret") {
		t.Fatalf("err = %v, want a value-free refusal", err)
	}
	for _, want := range []string{ghTokenEnv, ghTokenEnv + "_CMD", "SECRET_CMD"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	if strings.Contains(err.Error(), "fetch-token") {
		t.Errorf("error %q names the command", err)
	}
}

func TestResolveConnectorCredentials_UnreadRowsNeverRunCmd(t *testing.T) {
	butler := []daemon.Kind{daemon.KindOf(dispatchkind.Butler)}
	for _, tc := range []struct {
		name    string
		tracker string
		kinds   []daemon.Kind
	}{
		{"inactive backend", "local", workKinds},
		{"no tracker-probe kind", "github", butler},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearKnobEnvT(t)
			useGithubConnectorRow(t)
			t.Setenv(ghTokenEnv, "")
			t.Setenv(ghTokenEnv+"_CMD", "fetch-token")
			t.Setenv("SECRET_CMD", "rbw get spindrift-{name}")
			stubSecretCmd(t, failIfRun(t))
			refresh, err := resolveConnectorCredentials(demandDocT(map[string]string{"ISSUE_TRACKER": tc.tracker, "CODE_FORGE": "local"}), tc.kinds, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			if refresh != nil {
				t.Error("refresh is non-nil, want nil with nothing _CMD-sourced")
			}
		})
	}
}

const jiraTokenEnv = "JIRA_TOKEN"

func useJiraConnectorRow(t *testing.T) {
	t.Helper()
	t.Cleanup(backendrows.Snapshot())
	raw := `[{"name":"jira","module":"/nonexistent.wasm","credentialEnv":"` + jiraTokenEnv + `",
"routes":[{"method":"GET","host":"jira.example","path":"/x"}]}]`
	if err := backendrows.Use([]byte(raw), func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	t.Setenv(jiraTokenEnv, "")
	t.Setenv(jiraTokenEnv+"_CMD", "")
}

// jira's Strategy is ConnectorRouted, so its row's credential is in the
// Daemon's probe path.
func TestResolveConnectorCredentials_JiraRowResolves(t *testing.T) {
	clearKnobEnvT(t)
	useJiraConnectorRow(t)
	t.Setenv("SECRET_CMD", "vault get {name}")
	var ran []string
	stubSecretCmd(t, func(cmd string) (string, error) { ran = append(ran, cmd); return "tok\n", nil })
	if _, err := resolveConnectorCredentials(demandDocT(map[string]string{"ISSUE_TRACKER": "jira"}), workKinds, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(ran) == 0 {
		t.Error("secret command did not run, want the jira credential resolved")
	}
	if os.Getenv(jiraTokenEnv) != "tok" {
		t.Errorf("%s = %q, want tok", jiraTokenEnv, os.Getenv(jiraTokenEnv))
	}
}

func TestResolveConnectorCredentials_JiraRowRefusesWithoutCredential(t *testing.T) {
	clearKnobEnvT(t)
	useJiraConnectorRow(t)
	stubSecretCmd(t, failIfRun(t))
	_, err := resolveConnectorCredentials(demandDocT(map[string]string{"ISSUE_TRACKER": "jira"}), workKinds, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), jiraTokenEnv) {
		t.Fatalf("err = %v, want a refusal naming %s", err, jiraTokenEnv)
	}
}

func TestResolveConnectorCredentials_RefreshReResolvesCmdCredentials(t *testing.T) {
	clearKnobEnvT(t)
	useGithubConnectorRow(t)
	t.Setenv(ghTokenEnv, "")
	t.Setenv(ghTokenEnv+"_CMD", "fetch-token")
	out, fail := "tok-1", false
	stubSecretCmd(t, func(string) (string, error) {
		if fail {
			return "tok-leak", errors.New("exit")
		}
		return out, nil
	})
	var stderr bytes.Buffer
	refresh, err := resolveConnectorCredentials(demandDocT(map[string]string{"ISSUE_TRACKER": "github"}), workKinds, &stderr)
	if err != nil {
		t.Fatal(err)
	}

	out = "tok-2"
	refresh()
	if got := os.Getenv(ghTokenEnv); got != "tok-2" {
		t.Errorf("after refresh %s = %q, want tok-2", ghTokenEnv, got)
	}

	fail = true
	refresh()
	if got := os.Getenv(ghTokenEnv); got != "tok-2" {
		t.Errorf("after failed refresh %s = %q, want the old tok-2", ghTokenEnv, got)
	}
	if !strings.Contains(stderr.String(), ghTokenEnv) || strings.Contains(stderr.String(), "tok-") {
		t.Errorf("warning %q must name the credential and carry no secret", stderr.String())
	}
}

func TestResolveConnectorCredentials_PlainEnvIsNotRefreshed(t *testing.T) {
	clearKnobEnvT(t)
	useGithubConnectorRow(t)
	t.Setenv(ghTokenEnv, "plain")
	t.Setenv(ghTokenEnv+"_CMD", "")
	stubSecretCmd(t, failIfRun(t))
	refresh, err := resolveConnectorCredentials(demandDocT(map[string]string{"ISSUE_TRACKER": "github"}), workKinds, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if refresh != nil {
		t.Error("refresh is non-nil, want nil with nothing _CMD-sourced")
	}
	if got := os.Getenv(ghTokenEnv); got != "plain" {
		t.Errorf("%s = %q, want plain", ghTokenEnv, got)
	}
}

// writeConnectorDocT writes an input document carrying the github backend row
// (extra is spliced in as further row fields) and the settings.
func writeConnectorDocT(t *testing.T, extra string, settings map[string]string) string {
	t.Helper()
	merged := maps.Clone(shippedKnobDefaults)
	maps.Copy(merged, map[string]string{"DAEMON_APP": ".#dogfood", "BASE_BRANCH": "main", "MAX_PARALLEL": "1", "REPO_SLUG": "o/r"})
	maps.Copy(merged, settings)
	data, err := json.Marshal(inputdoc.Document{Settings: merged, Backends: json.RawMessage(
		`[{"name":"github","module":"/nonexistent.wasm","credentialEnv":"` + ghTokenEnv + `",
"routes":[{"method":"GET","host":"api.github.com","path":"/repos/{repo}/issues"}]` + extra + `}]`)})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "in.json")
	writeFileT(t, path, string(data))
	return path
}

// The refusal must precede --check-only's early return, or a successor's
// smoke test would pass a Daemon that then refuses to start.
func TestMainRun_ConnectorRowWithoutCredentialRefusesStart(t *testing.T) {
	for _, flags := range [][]string{{"dispatch"}, {"--check-only", "dispatch"}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			clearKnobEnvT(t)
			t.Cleanup(backendrows.Snapshot())
			stubSecretCmd(t, failIfRun(t))
			docPath := writeConnectorDocT(t, `,"credentialKnob":"ghToken"`, map[string]string{"ISSUE_TRACKER": "github"})

			var stdout, stderr bytes.Buffer
			if got := mainRun(append([]string{"--input", docPath}, flags...), &stdout, &stderr); got != 1 {
				t.Fatalf("mainRun() = %d, want 1; stderr = %q", got, stderr.String())
			}
			for _, want := range []string{ghTokenEnv + ",", ghTokenEnv + "_CMD", "SECRET_CMD", "ghToken"} {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
				}
			}
		})
	}
}

func TestMainRun_CheckOnlyRefusesAFailingCredentialCommand(t *testing.T) {
	clearKnobEnvT(t)
	t.Cleanup(backendrows.Snapshot())
	t.Setenv(ghTokenEnv+"_CMD", "fetch-token")
	stubSecretCmd(t, func(string) (string, error) { return "", errors.New("vault locked") })
	docPath := writeConnectorDocT(t, "", map[string]string{"ISSUE_TRACKER": "github"})

	var stdout, stderr bytes.Buffer
	if got := mainRun([]string{"--input", docPath, "--check-only", "dispatch"}, &stdout, &stderr); got != 1 {
		t.Fatalf("mainRun() = %d, want 1; stderr = %q", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), ghTokenEnv+"_CMD") {
		t.Errorf("stderr = %q, want it to name %s_CMD", stderr.String(), ghTokenEnv)
	}
}

func TestMainRun_UnreadConnectorRowsNeitherRunCmdNorRefuseStart(t *testing.T) {
	for _, tc := range []struct{ name, tracker string }{
		{"inactive backend", "local"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearKnobEnvT(t)
			checkOnlyNoNetworkT(t)
			t.Cleanup(backendrows.Snapshot())
			t.Setenv("SECRET_CMD", "rbw get spindrift-{name}")
			stubSecretCmd(t, failIfRun(t))
			docPath := writeConnectorDocT(t, "", map[string]string{"ISSUE_TRACKER": tc.tracker, "CODE_FORGE": "local"})

			var stdout, stderr bytes.Buffer
			if got := mainRun([]string{"--input", docPath, "--check-only", "dispatch"}, &stdout, &stderr); got != 0 {
				t.Fatalf("mainRun() = %d, want 0; stderr = %q", got, stderr.String())
			}
		})
	}
}

// The compiled github Connector and its Route table, as nix/checks/go.nix
// exports them for the launcher-binaries go test.
const (
	connectorWasmEnv   = "SPINDRIFT_BACKEND_CONNECTOR_WASM"
	connectorRoutesEnv = "SPINDRIFT_BACKEND_CONNECTOR_ROUTES"
)

// useCompiledGithubRow applies a github row running the compiled
// Connector with its Routes pointed at host, skipping when none was built.
func useCompiledGithubRow(t *testing.T, host string) {
	t.Helper()
	wasm := os.Getenv(connectorWasmEnv)
	if wasm == "" {
		t.Skipf("%s unset: no compiled github Connector to test", connectorWasmEnv)
	}
	var routes []connectorhost.Route
	if err := json.Unmarshal([]byte(os.Getenv(connectorRoutesEnv)), &routes); err != nil {
		t.Fatalf("%s: %v", connectorRoutesEnv, err)
	}
	for i := range routes {
		routes[i].Host = host
	}
	raw, err := json.Marshal([]backendrows.Row{{
		Name: "github", Module: wasm, Routes: routes, CredentialEnv: ghTokenEnv,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backendrows.Snapshot())
	if err := backendrows.Use(raw, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
}

// With only the credential's `_CMD` form set, the Daemon's Demand probe counts
// ready issues through the Connector row, authenticated by the token
// the command printed.
func TestDemandProbe_CountsThroughTheConnectorWithACmdCredential(t *testing.T) {
	clearKnobEnvT(t)
	var mu sync.Mutex
	var auths []string
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/repos/o/r/issues" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[{"number":1,"title":"a","state":"open"},{"number":2,"title":"b","state":"open"},
{"number":3,"title":"pr","state":"open","pull_request":{}}]`))
	}))
	defer api.Close()
	useCompiledGithubRow(t, api.Listener.Addr().String())
	defer github.ConnectorClientForTest(api.Client())()
	t.Chdir(t.TempDir())
	t.Setenv(ghTokenEnv, "")
	t.Setenv(ghTokenEnv+"_CMD", "fetch-token")
	stubSecretCmd(t, func(string) (string, error) { return "tok-1\n", nil })

	doc := demandDocT(map[string]string{"ISSUE_TRACKER": "github", "REPO_SLUG": "o/r"})
	if _, err := resolveConnectorCredentials(doc, workKinds, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	sources, missing := buildDemandSources(doc, workKinds)
	if len(missing) != 0 {
		t.Fatalf("missing sources: %v", missing)
	}
	got, err := sources[workKinds[0]].CountReady(false)
	if err != nil {
		t.Fatalf("CountReady: %v", err)
	}
	if got != 2 {
		t.Errorf("CountReady = %d, want 2", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auths) == 0 {
		t.Error("the fake GitHub saw no request")
	}
}

func stubConnectorWatch(t *testing.T) *[]func() {
	t.Helper()
	var started []func()
	saved := startConnectorCredentialWatch
	startConnectorCredentialWatch = func(refresh func(), _ time.Duration) { started = append(started, refresh) }
	t.Cleanup(func() { startConnectorCredentialWatch = saved })
	return &started
}

// mainRun is driven from a non-git directory so it refuses at the checkout
// lookup, after the watcher start and before the lock and preflight.
func TestMainRun_StartsTheConnectorCredentialWatcherForACmdSourcedRow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		want  int
	}{
		{"running", []string{"dispatch"}, 1},
		{"check-only", []string{"--check-only", "dispatch"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearKnobEnvT(t)
			t.Cleanup(backendrows.Snapshot())
			t.Setenv(ghTokenEnv+"_CMD", "fetch-token")
			stubSecretCmd(t, func(string) (string, error) { return "tok", nil })
			started := stubConnectorWatch(t)
			docPath := writeConnectorDocT(t, "", map[string]string{"ISSUE_TRACKER": "github"})
			t.Chdir(t.TempDir())

			var stdout, stderr bytes.Buffer
			if got := mainRun(append([]string{"--input", docPath}, tc.flags...), &stdout, &stderr); got != tc.want {
				t.Fatalf("mainRun() = %d, want %d; stderr = %q", got, tc.want, stderr.String())
			}
			wantStarted := tc.name == "running"
			if got := len(*started) == 1 && (*started)[0] != nil; got != wantStarted {
				t.Errorf("watcher started with a refresh = %v (%d starts), want %v", got, len(*started), wantStarted)
			}
		})
	}
}

// A forgejo row is read as the CODE_FORGE and as the ISSUE_TRACKER, so the
// credential gate resolves its credential on either axis.
func TestResolveConnectorCredentials_ForgejoRowResolvesAsForgeOrTracker(t *testing.T) {
	const forgejoTokenEnv = "FORGEJO_TOKEN"
	for _, tc := range []struct {
		name, template, forge string
		wantRan, wantErr      bool
	}{
		{"unset refuses", "", "forgejo", false, true},
		{"forge resolves", "rbw get {name}", "forgejo", true, false},
		{"tracker only resolves", "rbw get {name}", "git", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearKnobEnvT(t)
			t.Cleanup(backendrows.Snapshot())
			raw := `[{"name":"forgejo","module":"/nonexistent.wasm","credentialEnv":"` + forgejoTokenEnv + `",
"routes":[{"method":"GET","host":"codeberg.org","path":"/api/v1/x"}]}]`
			if err := backendrows.Use([]byte(raw), func(string) string { return "" }); err != nil {
				t.Fatal(err)
			}
			t.Setenv(forgejoTokenEnv, "")
			t.Setenv(forgejoTokenEnv+"_CMD", "")
			t.Setenv("SECRET_CMD", tc.template)
			var ran bool
			stubSecretCmd(t, func(string) (string, error) { ran = true; return "tok\n", nil })
			tracker := "local"
			if tc.forge != "forgejo" {
				tracker = "forgejo"
			}
			doc := demandDocT(map[string]string{"CODE_FORGE": tc.forge, "ISSUE_TRACKER": tracker})

			_, err := resolveConnectorCredentials(doc, workKinds, &bytes.Buffer{})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error: %v", err, tc.wantErr)
			}
			if ran != tc.wantRan {
				t.Errorf("secret command ran = %v, want %v", ran, tc.wantRan)
			}
			if got := os.Getenv(forgejoTokenEnv); tc.wantRan && got != "tok" {
				t.Errorf("%s = %q, want tok", forgejoTokenEnv, got)
			}
		})
	}
}

// FORGEJO_TOKEN is a core secret yet the daemon resolves it through the
// forgejo row, so its `_CMD` form fills it as a Connector credential does.
func TestResolveConnectorCredentials_ForgejoTokenCmdFillsFORGEJO_TOKEN(t *testing.T) {
	clearKnobEnvT(t)
	t.Cleanup(backendrows.Snapshot())
	raw := `[{"name":"forgejo","module":"/nonexistent.wasm","credentialEnv":"FORGEJO_TOKEN",
"routes":[{"method":"GET","host":"codeberg.org","path":"/api/v1/x"}]}]`
	if err := backendrows.Use([]byte(raw), func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGEJO_TOKEN", "")
	t.Setenv("FORGEJO_TOKEN_CMD", "fetch-forgejo-token")
	t.Setenv("SECRET_CMD", "")
	var ranCmd string
	stubSecretCmd(t, func(cmd string) (string, error) { ranCmd = cmd; return "tok-f\n", nil })
	doc := demandDocT(map[string]string{"CODE_FORGE": "forgejo", "ISSUE_TRACKER": "local"})

	if _, err := resolveConnectorCredentials(doc, workKinds, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if ranCmd != "fetch-forgejo-token" {
		t.Errorf("ran %q, want fetch-forgejo-token", ranCmd)
	}
	if got := os.Getenv("FORGEJO_TOKEN"); got != "tok-f" {
		t.Errorf("FORGEJO_TOKEN = %q, want tok-f", got)
	}
}

// A routed github row is read as the CODE_FORGE through its Connector, so its
// credential resolves even with a local tracker.
func TestResolveConnectorCredentials_GithubForgeRowResolves(t *testing.T) {
	clearKnobEnvT(t)
	useGithubConnectorRow(t)
	t.Setenv(ghTokenEnv, "")
	t.Setenv(ghTokenEnv+"_CMD", "")
	t.Setenv("SECRET_CMD", "rbw get {name}")
	var ran bool
	stubSecretCmd(t, func(string) (string, error) { ran = true; return "tok\n", nil })
	doc := demandDocT(map[string]string{"CODE_FORGE": "github", "ISSUE_TRACKER": "local"})

	if _, err := resolveConnectorCredentials(doc, workKinds, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Error("secret command did not run, want the github credential resolved")
	}
}

// Under a GH_HOST other than github.com the github gate drops the row (its
// Routes pin api.github.com), so gh serves it and the Daemon resolves no
// Connector credential for it.
func TestResolveConnectorCredentials_GithubRowGatedOffUnderGHEHostRunsNoCmd(t *testing.T) {
	clearKnobEnvT(t)
	t.Cleanup(backendrows.Snapshot())
	raw := `[{"name":"github","module":"/nonexistent.wasm","credentialEnv":"` + ghTokenEnv + `",
"routes":[{"method":"GET","host":"api.github.com","path":"/repos/{repo}/issues"}]}]`
	ghe := func(k string) string {
		if k == "GH_HOST" {
			return "ghe.example.com"
		}
		return ""
	}
	if err := backendrows.Use([]byte(raw), ghe); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ghTokenEnv, "")
	t.Setenv(ghTokenEnv+"_CMD", "fetch-token")
	stubSecretCmd(t, failIfRun(t))

	refresh, err := resolveConnectorCredentials(demandDocT(map[string]string{"ISSUE_TRACKER": "github"}), workKinds, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if refresh != nil {
		t.Error("a credential refresh was set up for a row the GH_HOST gate dropped")
	}
}
