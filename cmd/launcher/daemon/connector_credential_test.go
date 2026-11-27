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

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectorsetup"
	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge/github"
	"spindrift.dev/launcher/internal/inputdoc"
)

// ghTokenEnv is the github row's one credential: core's own GH_TOKEN.
const ghTokenEnv = "GH_TOKEN"

// testRows are the backend rows the test's last useRows decoded, the rows
// connectorInputT hands the runtime.
var testRows backendrows.Set

// useRows decodes raw under the registered row gates and lookup, as mainRun
// does, and keeps the Set for connectorInputT until the test ends.
func useRows(t *testing.T, raw []byte, lookup func(string) string) backendrows.Set {
	t.Helper()
	set, err := backendrows.Decode(raw, lookup, backendkit.RowGates())
	if err != nil {
		t.Fatal(err)
	}
	prev := testRows
	testRows = set
	t.Cleanup(func() { testRows = prev })
	return set
}

// connectorInputT is connectorInput over the rows useRows decoded.
func connectorInputT(doc *inputdoc.Document, kinds []daemon.Kind, pwd string) connectorsetup.Input {
	in := connectorInput(doc, kinds, pwd)
	in.Rows = testRows
	return in
}

// useGithubConnectorRow decodes a github backend row.
func useGithubConnectorRow(t *testing.T) {
	t.Helper()
	raw := `[{"name":"github","module":"/nonexistent.wasm","credentialEnv":"` + ghTokenEnv + `",
"routes":[{"method":"GET","host":"api.github.com","path":"/repos/{repo}/issues"}]}]`
	useRows(t, []byte(raw), func(string) string { return "" })
}

// stubSecretCmd runs every credential `_CMD` the runtime resolves through run.
func stubSecretCmd(t *testing.T, run func(string) (string, error)) {
	t.Helper()
	build := newConnectorRuntime
	newConnectorRuntime = func(in connectorsetup.Input, opts connectorsetup.Options) (*connectorruntime.Runtime, error) {
		opts.Run = run
		return build(in, opts)
	}
	t.Cleanup(func() { newConnectorRuntime = build })
}

// connectorRuntimeT builds the runtime mainRun would for doc and kinds, closing
// it with the test.
func connectorRuntimeT(t *testing.T, doc *inputdoc.Document, kinds []daemon.Kind) (*connectorruntime.Runtime, error) {
	t.Helper()
	rt, err := newConnectorRuntime(connectorInputT(doc, kinds, t.TempDir()), connectorsetup.Options{RequireCredential: true})
	if err == nil {
		t.Cleanup(func() { _ = rt.Close() })
	}
	return rt, err
}

func failIfRun(t *testing.T) func(string) (string, error) {
	return func(cmd string) (string, error) {
		t.Errorf("secret command ran: %q", cmd)
		return "", errors.New("must not run")
	}
}

var workKinds = []daemon.Kind{daemon.KindOf(dispatchkind.Work)}

func TestConnectorRuntime_CmdFormFillsCredentialEnv(t *testing.T) {
	clearKnobEnvT(t)
	useGithubConnectorRow(t)
	t.Setenv(ghTokenEnv, "")
	t.Setenv(ghTokenEnv+"_CMD", "fetch-token")
	var ran string
	stubSecretCmd(t, func(cmd string) (string, error) { ran = cmd; return "tok-1\n", nil })

	rt, err := connectorRuntimeT(t, demandDocT(map[string]string{"ISSUE_TRACKER": "github"}), workKinds)
	if err != nil {
		t.Fatal(err)
	}
	if ran != "fetch-token" {
		t.Errorf("ran %q, want fetch-token", ran)
	}
	if got := tokenOf(rt, "github"); got != "tok-1" {
		t.Errorf("github credential = %q, want tok-1", got)
	}
	if got := os.Getenv(ghTokenEnv); got != "" {
		t.Errorf("%s = %q, want the process environment untouched", ghTokenEnv, got)
	}
}

func TestConnectorRuntime_CmdWinsOverPlainEnv(t *testing.T) {
	clearKnobEnvT(t)
	useGithubConnectorRow(t)
	t.Setenv(ghTokenEnv, "stale")
	t.Setenv(ghTokenEnv+"_CMD", "fetch-token")
	stubSecretCmd(t, func(string) (string, error) { return "fresh\n", nil })
	rt, err := connectorRuntimeT(t, demandDocT(map[string]string{"ISSUE_TRACKER": "github"}), workKinds)
	if err != nil {
		t.Fatal(err)
	}
	if got := tokenOf(rt, "github"); got != "fresh" {
		t.Errorf("github credential = %q, want fresh", got)
	}
}

func TestConnectorRuntime_SecretCmdTemplateAloneResolves(t *testing.T) {
	clearKnobEnvT(t)
	useGithubConnectorRow(t)
	t.Setenv(ghTokenEnv, "")
	t.Setenv(ghTokenEnv+"_CMD", "")
	t.Setenv("SECRET_CMD", "rbw get spindrift-{name}")
	out := "tok-1"
	var ran string
	stubSecretCmd(t, func(cmd string) (string, error) { ran = cmd; return out, nil })
	doc := demandDocT(map[string]string{"ISSUE_TRACKER": "github"})

	rt, err := connectorRuntimeT(t, doc, workKinds)
	if err != nil {
		t.Fatal(err)
	}
	if ran != "rbw get spindrift-gh-token" {
		t.Errorf("ran %q, want the template rendered for the credential env", ran)
	}
	if got := tokenOf(rt, "github"); got != "tok-1" {
		t.Errorf("github credential = %q, want tok-1", got)
	}

	// Nothing was exported, so a restart-in-place successor re-resolves.
	out = "tok-2"
	succ, err := connectorRuntimeT(t, doc, workKinds)
	if err != nil || tokenOf(succ, "github") != "tok-2" {
		t.Errorf("successor credential = %q, %v; want tok-2", tokenOf(succ, "github"), err)
	}
}

func TestConnectorRuntime_RefusesWhenNeitherFormSet(t *testing.T) {
	clearKnobEnvT(t)
	useGithubConnectorRow(t)
	t.Setenv(ghTokenEnv, "")
	t.Setenv(ghTokenEnv+"_CMD", "")
	stubSecretCmd(t, failIfRun(t))
	_, err := connectorRuntimeT(t, demandDocT(map[string]string{"ISSUE_TRACKER": "github"}), workKinds)
	if err == nil {
		t.Fatal("want a refusal")
	}
	for _, want := range []string{ghTokenEnv, ghTokenEnv + "_CMD", "SECRET_CMD"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

func TestConnectorRuntime_CmdFailureRefusesWithoutSecret(t *testing.T) {
	clearKnobEnvT(t)
	useGithubConnectorRow(t)
	t.Setenv(ghTokenEnv, "")
	t.Setenv(ghTokenEnv+"_CMD", "fetch-token")
	stubSecretCmd(t, func(string) (string, error) { return "partial-secret", errors.New("boom partial-secret") })
	_, err := connectorRuntimeT(t, demandDocT(map[string]string{"ISSUE_TRACKER": "github"}), workKinds)
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

func TestConnectorRuntime_UnreadRowsNeverRunCmd(t *testing.T) {
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
			_, err := connectorRuntimeT(t, demandDocT(map[string]string{"ISSUE_TRACKER": tc.tracker, "CODE_FORGE": "local"}), tc.kinds)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

const jiraTokenEnv = "JIRA_TOKEN"

func useJiraConnectorRow(t *testing.T) {
	t.Helper()
	raw := `[{"name":"jira","module":"/nonexistent.wasm","credentialEnv":"` + jiraTokenEnv + `",
"routes":[{"method":"GET","host":"jira.example","path":"/x"}]}]`
	useRows(t, []byte(raw), func(string) string { return "" })
	t.Setenv(jiraTokenEnv, "")
	t.Setenv(jiraTokenEnv+"_CMD", "")
}

// jira's Strategy is ConnectorRouted, so its row's credential is in the
// Daemon's probe path.
func TestConnectorRuntime_JiraRowResolves(t *testing.T) {
	clearKnobEnvT(t)
	useJiraConnectorRow(t)
	t.Setenv("SECRET_CMD", "vault get {name}")
	var ran []string
	stubSecretCmd(t, func(cmd string) (string, error) { ran = append(ran, cmd); return "tok\n", nil })
	rt, err := connectorRuntimeT(t, demandDocT(map[string]string{"ISSUE_TRACKER": "jira"}), workKinds)
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) == 0 {
		t.Error("secret command did not run, want the jira credential resolved")
	}
	if got := tokenOf(rt, "jira"); got != "tok" {
		t.Errorf("jira credential = %q, want tok", got)
	}
}

func TestConnectorRuntime_JiraRowRefusesWithoutCredential(t *testing.T) {
	clearKnobEnvT(t)
	useJiraConnectorRow(t)
	stubSecretCmd(t, failIfRun(t))
	_, err := connectorRuntimeT(t, demandDocT(map[string]string{"ISSUE_TRACKER": "jira"}), workKinds)
	if err == nil || !strings.Contains(err.Error(), jiraTokenEnv) {
		t.Fatalf("err = %v, want a refusal naming %s", err, jiraTokenEnv)
	}
}

func TestConnectorRuntime_PlainEnvIsReadWithoutCmd(t *testing.T) {
	clearKnobEnvT(t)
	useGithubConnectorRow(t)
	t.Setenv(ghTokenEnv, "plain")
	t.Setenv(ghTokenEnv+"_CMD", "")
	stubSecretCmd(t, failIfRun(t))
	rt, err := connectorRuntimeT(t, demandDocT(map[string]string{"ISSUE_TRACKER": "github"}), workKinds)
	if err != nil {
		t.Fatal(err)
	}
	if got := tokenOf(rt, "github"); got != "plain" {
		t.Errorf("github credential = %q, want plain", got)
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
	useRows(t, raw, func(string) string { return "" })
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
	rt, err := connectorRuntimeT(t, doc, workKinds)
	if err != nil {
		t.Fatal(err)
	}
	sources, missing := buildDemandSources(rt, doc, workKinds)
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

// mainRun is driven from a non-git directory so it refuses at the checkout
// lookup, after the runtime is built and before the lock and preflight.
func TestMainRun_BuildsTheRuntimeRefreshingCmdCredentials(t *testing.T) {
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
			t.Setenv(ghTokenEnv+"_CMD", "fetch-token")
			stubSecretCmd(t, func(string) (string, error) { return "tok", nil })
			var built []connectorsetup.Options
			build := newConnectorRuntime
			newConnectorRuntime = func(in connectorsetup.Input, opts connectorsetup.Options) (*connectorruntime.Runtime, error) {
				built = append(built, opts)
				return build(in, opts)
			}
			t.Cleanup(func() { newConnectorRuntime = build })
			docPath := writeConnectorDocT(t, "", map[string]string{"ISSUE_TRACKER": "github"})
			t.Chdir(t.TempDir())

			var stdout, stderr bytes.Buffer
			if got := mainRun(append([]string{"--input", docPath}, tc.flags...), &stdout, &stderr); got != tc.want {
				t.Fatalf("mainRun() = %d, want %d; stderr = %q", got, tc.want, stderr.String())
			}
			if len(built) != 1 || built[0].RefreshEvery != connectorCredentialRefresh || !built[0].RequireCredential {
				t.Errorf("runtime options = %+v, want one build refreshing every %s and refusing an unset credential", built, connectorCredentialRefresh)
			}
		})
	}
}

// A forgejo row is read as the CODE_FORGE and as the ISSUE_TRACKER, so the
// credential gate resolves its credential on either axis.
func TestConnectorRuntime_ForgejoRowResolvesAsForgeOrTracker(t *testing.T) {
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
			raw := `[{"name":"forgejo","module":"/nonexistent.wasm","credentialEnv":"` + forgejoTokenEnv + `",
"routes":[{"method":"GET","host":"codeberg.org","path":"/api/v1/x"}]}]`
			useRows(t, []byte(raw), func(string) string { return "" })
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

			rt, err := connectorRuntimeT(t, doc, workKinds)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error: %v", err, tc.wantErr)
			}
			if ran != tc.wantRan {
				t.Errorf("secret command ran = %v, want %v", ran, tc.wantRan)
			}
			if got := tokenOf(rt, "forgejo"); tc.wantRan && got != "tok" {
				t.Errorf("forgejo credential = %q, want tok", got)
			}
		})
	}
}

// FORGEJO_TOKEN is a core secret yet the daemon resolves it through the
// forgejo row, so its `_CMD` form fills it as a Connector credential does.
func TestConnectorRuntime_ForgejoTokenCmdFillsFORGEJO_TOKEN(t *testing.T) {
	clearKnobEnvT(t)
	raw := `[{"name":"forgejo","module":"/nonexistent.wasm","credentialEnv":"FORGEJO_TOKEN",
"routes":[{"method":"GET","host":"codeberg.org","path":"/api/v1/x"}]}]`
	useRows(t, []byte(raw), func(string) string { return "" })
	t.Setenv("FORGEJO_TOKEN", "")
	t.Setenv("FORGEJO_TOKEN_CMD", "fetch-forgejo-token")
	t.Setenv("SECRET_CMD", "")
	var ranCmd string
	stubSecretCmd(t, func(cmd string) (string, error) { ranCmd = cmd; return "tok-f\n", nil })
	doc := demandDocT(map[string]string{"CODE_FORGE": "forgejo", "ISSUE_TRACKER": "local"})

	rt, err := connectorRuntimeT(t, doc, workKinds)
	if err != nil {
		t.Fatal(err)
	}
	if ranCmd != "fetch-forgejo-token" {
		t.Errorf("ran %q, want fetch-forgejo-token", ranCmd)
	}
	if got := tokenOf(rt, "forgejo"); got != "tok-f" {
		t.Errorf("forgejo credential = %q, want tok-f", got)
	}
}

// A routed github row is read as the CODE_FORGE through its Connector, so its
// credential resolves even with a local tracker.
func TestConnectorRuntime_GithubForgeRowResolves(t *testing.T) {
	clearKnobEnvT(t)
	useGithubConnectorRow(t)
	t.Setenv(ghTokenEnv, "")
	t.Setenv(ghTokenEnv+"_CMD", "")
	t.Setenv("SECRET_CMD", "rbw get {name}")
	var ran bool
	stubSecretCmd(t, func(string) (string, error) { ran = true; return "tok\n", nil })
	doc := demandDocT(map[string]string{"CODE_FORGE": "github", "ISSUE_TRACKER": "local"})

	_, err := connectorRuntimeT(t, doc, workKinds)
	if err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Error("secret command did not run, want the github credential resolved")
	}
}

// Under a GH_HOST other than github.com the github gate drops the row (its
// Routes pin api.github.com), so gh serves it and the Daemon resolves no
// Connector credential for it.
func TestConnectorRuntime_GithubRowGatedOffUnderGHEHostRunsNoCmd(t *testing.T) {
	clearKnobEnvT(t)
	raw := `[{"name":"github","module":"/nonexistent.wasm","credentialEnv":"` + ghTokenEnv + `",
"routes":[{"method":"GET","host":"api.github.com","path":"/repos/{repo}/issues"}]}]`
	ghe := func(k string) string {
		if k == "GH_HOST" {
			return "ghe.example.com"
		}
		return ""
	}
	useRows(t, []byte(raw), ghe)
	t.Setenv(ghTokenEnv, "")
	t.Setenv(ghTokenEnv+"_CMD", "fetch-token")
	stubSecretCmd(t, failIfRun(t))

	_, err := connectorRuntimeT(t, demandDocT(map[string]string{"ISSUE_TRACKER": "github"}), workKinds)
	if err != nil {
		t.Fatal(err)
	}
}

// tokenOf is the secret the runtime would read for the named row.
func tokenOf(rt *connectorruntime.Runtime, name string) string {
	if rt == nil {
		return ""
	}
	row, ok := rt.Row(name)
	if !ok {
		return ""
	}
	cred, err := rt.GitCredential(row)()
	if err != nil {
		return ""
	}
	return cred.Password
}

// GH_TOKEN_REFRESH_FILE reaches the Connector runtime from the Daemon's
// document or its environment: a token rewritten into the file is the github
// row's next git credential, with no GH_TOKEN of its own.
func TestConnectorRuntime_RefreshFileRotatesGitCredential(t *testing.T) {
	for _, tier := range []string{"document", "environment"} {
		t.Run(tier, func(t *testing.T) {
			clearKnobEnvT(t)
			useGithubConnectorRow(t)
			t.Setenv(ghTokenEnv, "")
			t.Setenv(ghTokenEnv+"_CMD", "")
			stubSecretCmd(t, failIfRun(t))
			path := filepath.Join(t.TempDir(), "gh-token")
			settings := map[string]string{"CODE_FORGE": "github", "ISSUE_TRACKER": "local"}
			if tier == "document" {
				settings["GH_TOKEN_REFRESH_FILE"] = path
			} else {
				t.Setenv("GH_TOKEN_REFRESH_FILE", path)
			}

			rt, err := connectorRuntimeT(t, demandDocT(settings), workKinds)
			if err != nil {
				t.Fatalf("a refresh file stands in for an unset GH_TOKEN: %v", err)
			}
			row, ok := rt.Row("github")
			if !ok {
				t.Fatal("no github row")
			}
			git := rt.GitCredential(row)

			for _, token := range []string{"tok-one", "tok-two"} {
				if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if cred, err := git(); err != nil || cred.Password != token {
					t.Fatalf("after writing %s: GitCredential = (%v, %v), want password %s", token, cred, err, token)
				}
			}
		})
	}
}
