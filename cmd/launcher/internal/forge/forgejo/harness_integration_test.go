//go:build integration

package forgejo_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
)

// forgejoHarnessLabel is one label the harness seeds into a fresh Forgejo
// repo. Color is a bare 6-hex-digit string with no leading "#", because the
// adapter's CreateLabel prepends the "#" itself.
type forgejoHarnessLabel struct {
	Name  string
	Color string
}

// forgejoHarnessLabels returns the label set the harness seeds: the
// triage/dispatch family (testLabels, mirrored from lib/env-schema.nix) plus
// the research verdict family.
func forgejoHarnessLabels() []forgejoHarnessLabel {
	labels := []forgejoHarnessLabel{
		{Name: testLabels.Dispatchable, Color: "0e8a16"},
		{Name: testLabels.InProgress, Color: "fbca04"},
		{Name: testLabels.Complete, Color: "0052cc"},
		{Name: testLabels.Failed, Color: "b60205"},
	}
	for _, entry := range forge.ResearchVerdictLabels().Entries() {
		labels = append(labels, forgejoHarnessLabel{Name: entry.Label, Color: "5319e7"})
	}
	return labels
}

// forgejoContainerRunArgs builds the argv (without the leading cli) for a
// detached, throwaway Forgejo container: SQLite, install lock pre-set so first
// boot skips the setup wizard, and --rm so a crashed run leaves no stopped
// container behind. hostPort 0 omits the host side of the publish flag, so the
// runtime picks an ephemeral port (recovered via parseForgejoHostPort).
func forgejoContainerRunArgs(cli, name, image string, hostPort int) []string {
	publish := "127.0.0.1::3000"
	if hostPort != 0 {
		publish = fmt.Sprintf("127.0.0.1:%d:3000", hostPort)
	}
	return []string{
		"run", "-d", "--rm",
		"--name", name,
		"-p", publish,
		"-e", "FORGEJO__security__INSTALL_LOCK=true",
		"-e", "FORGEJO__database__DB_TYPE=sqlite3",
		"-e", "FORGEJO__server__HTTP_PORT=3000",
		image,
	}
}

// parseForgejoHostPort reads `docker port <name> 3000` output, one
// "<host>:<port>" mapping per line ("0.0.0.0:49153", possibly with an ipv6
// "[::]:49153" line alongside), and returns the first parseable port. Empty or
// unparseable input errors rather than returning a zero port, since a harness
// that cannot discover the published port has nothing to poll.
func parseForgejoHostPort(portCmdOutput string) (int, error) {
	for _, line := range strings.Split(portCmdOutput, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idx := strings.LastIndex(line, ":")
		if idx < 0 || idx == len(line)-1 {
			continue
		}
		port, err := strconv.Atoi(line[idx+1:])
		if err != nil {
			continue
		}
		return port, nil
	}
	return 0, fmt.Errorf("forgejo: parse host port: no parseable \"host:port\" line in %q", portCmdOutput)
}

// forgejoAdminCreateArgs builds the argv (without cli/exec/container) for
// `forgejo admin user create`. --must-change-password=false is required
// because the harness has no interactive terminal to satisfy a forced
// password change on first login.
func forgejoAdminCreateArgs(username, password, email string) []string {
	return []string{
		"forgejo", "admin", "user", "create",
		"--admin",
		"--username", username,
		"--password", password,
		"--email", email,
		"--must-change-password=false",
	}
}

// forgejoTokenGenArgs builds the argv (without cli/exec/container) for
// `forgejo admin user generate-access-token`. --raw prints the bare token to
// stdout instead of a table, so the harness can capture it without parsing.
func forgejoTokenGenArgs(username string) []string {
	return []string{
		"forgejo", "admin", "user", "generate-access-token",
		"--username", username,
		"--scopes", "all",
		"--raw",
		"--token-name", "spindrift-harness",
	}
}

func forgejoVersionURL(baseURL string) string {
	return strings.TrimSuffix(baseURL, "/") + "/api/v1/version"
}

// requireForgejoRuntime returns the CLI name ("podman" or "docker") for the
// first runtime on PATH with a reachable daemon, skipping cleanly when neither
// is usable. It mirrors internal/runner's requireRealOCI (issue #576, "skip on
// hosts with no real runtime"), reimplemented here so this package need not
// import internal/runner for one small probe.
func requireForgejoRuntime(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("forgejo integration harness requires Linux")
	}
	for _, cli := range []string{"podman", "docker"} {
		if _, err := exec.LookPath(cli); err != nil {
			continue
		}
		if err := exec.Command(cli, "info").Run(); err != nil {
			continue
		}
		return cli
	}
	t.Skip("neither podman nor docker has a reachable daemon on PATH")
	return ""
}

// defaultForgejoHarnessImage is the pinned Forgejo OCI image the harness
// boots. It is public, so booting the harness never needs a registry
// credential. SPINDRIFT_FORGEJO_IMAGE overrides it for a moved tag or a
// mirror.
const defaultForgejoHarnessImage = "codeberg.org/forgejo/forgejo:11"

func forgejoHarnessImage() string {
	if img := os.Getenv("SPINDRIFT_FORGEJO_IMAGE"); img != "" {
		return img
	}
	return defaultForgejoHarnessImage
}

// forgejoBootTimeout bounds launching the harness container, covering the
// implicit image pull on a cold cache.
const forgejoBootTimeout = 90 * time.Second

// forgejoPortTimeout bounds polling `<cli> port` for the runtime to publish
// the container's ephemeral host port.
const forgejoPortTimeout = 10 * time.Second

// forgejoReadyTimeout bounds polling the version endpoint for the freshly
// started container to start answering requests.
const forgejoReadyTimeout = 60 * time.Second

// forgejoHTTPTimeout bounds a single harness HTTP request so one hung
// connect or read cannot block past the surrounding readiness deadline
// (forgejoWaitReady) or go test's global timeout (doREST).
const forgejoHTTPTimeout = 10 * time.Second

// forgejoHTTPClient replaces http.DefaultClient, whose timeout is unbounded,
// for every harness HTTP call.
var forgejoHTTPClient = &http.Client{Timeout: forgejoHTTPTimeout}

// forgejoAdminBootRetries bounds retries of the admin-user bootstrap command,
// since the database may not have finished migrating the instant /version
// starts answering 200.
const forgejoAdminBootRetries = 5

// The harness's throwaway admin credentials never leave the disposable,
// localhost-only container it boots and tears down within one test.
const (
	forgejoAdminUser     = "root"
	forgejoAdminPassword = "spindrift-harness-pw"
	forgejoAdminEmail    = "root@harness.local"
)

// isForgejoRuntimeUnavailable reports whether a failed container launch means
// the registry or daemon is unreachable (a CI runner with no network egress,
// an unauthenticated pull quota, a broken local daemon) rather than a harness
// bug. bootForgejo skips on that signal instead of failing hard.
func isForgejoRuntimeUnavailable(output string) bool {
	for _, s := range []string{
		"no such host",
		"connection refused",
		"TLS handshake timeout",
		"i/o timeout",
		"pull access denied",
		"manifest unknown",
		"toomanyrequests",
		"Cannot connect to the Docker daemon",
		"OCI runtime error",
	} {
		if strings.Contains(output, s) {
			return true
		}
	}
	return false
}

// bootForgejo registers the container-removal cleanup as soon as the launch
// itself succeeds, so a later failure (readiness timeout, bootstrap failure)
// still tears the container down.
func bootForgejo(t *testing.T, cli string) (baseURL, token string) {
	t.Helper()

	name := fmt.Sprintf("spindrift-forgejo-harness-%d", time.Now().UnixNano())

	ctx, cancel := context.WithTimeout(context.Background(), forgejoBootTimeout)
	defer cancel()
	args := forgejoContainerRunArgs(cli, name, forgejoHarnessImage(), 0)
	out, err := exec.CommandContext(ctx, cli, args...).CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded || isForgejoRuntimeUnavailable(string(out)) {
			t.Skipf("forgejo harness: %s run unavailable: %v: %s", cli, err, out)
		}
		t.Fatalf("forgejo harness: %s run failed: %v: %s", cli, err, out)
	}
	t.Cleanup(func() { _ = exec.Command(cli, "rm", "-f", name).Run() })

	port := forgejoWaitForPort(t, cli, name)
	baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	forgejoWaitReady(t, baseURL)
	forgejoBootstrapAdmin(t, cli, name)
	token = forgejoMintToken(t, cli, name)
	return baseURL, token
}

// forgejoWaitForPort polls `<cli> port <name> 3000` until the runtime reports
// the published host port. The mapping may not be queryable for a moment
// after `run -d` returns.
func forgejoWaitForPort(t *testing.T, cli, name string) int {
	t.Helper()
	deadline := time.Now().Add(forgejoPortTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		out, err := exec.Command(cli, "port", name, "3000").Output()
		if err != nil {
			lastErr = err
		} else if port, perr := parseForgejoHostPort(string(out)); perr != nil {
			lastErr = perr
		} else {
			return port
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("forgejo harness: %s never published a host port for %s: %v", cli, name, lastErr)
	return 0
}

func forgejoWaitReady(t *testing.T, baseURL string) {
	t.Helper()
	url := forgejoVersionURL(baseURL)
	deadline := time.Now().Add(forgejoReadyTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := forgejoHTTPClient.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			lastErr = fmt.Errorf("unexpected status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("forgejo harness: %s never became ready: %v", url, lastErr)
}

func forgejoExecArgs(name string, cmd []string) []string {
	return append([]string{"exec", "-u", "git", name}, cmd...)
}

// forgejoBootstrapAdmin runs `forgejo admin user create` inside the harness
// container, retrying because the database may not have finished migrating
// the instant the version endpoint answers 200. A "user already exists"
// failure, a retry racing its own successful prior attempt, counts as success.
func forgejoBootstrapAdmin(t *testing.T, cli, name string) {
	t.Helper()
	args := forgejoExecArgs(name, forgejoAdminCreateArgs(forgejoAdminUser, forgejoAdminPassword, forgejoAdminEmail))
	var lastOut []byte
	var lastErr error
	for attempt := 0; attempt < forgejoAdminBootRetries; attempt++ {
		out, err := exec.Command(cli, args...).CombinedOutput()
		if err == nil {
			return
		}
		if strings.Contains(string(out), "already exists") {
			return
		}
		lastOut, lastErr = out, err
		time.Sleep(time.Second)
	}
	t.Fatalf("forgejo harness: admin bootstrap failed after %d attempts: %v: %s", forgejoAdminBootRetries, lastErr, lastOut)
}

func forgejoMintToken(t *testing.T, cli, name string) string {
	t.Helper()
	args := forgejoExecArgs(name, forgejoTokenGenArgs(forgejoAdminUser))
	out, err := exec.Command(cli, args...).Output()
	if err != nil {
		t.Fatalf("forgejo harness: token mint failed: %v", err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		t.Fatalf("forgejo harness: token mint produced an empty token")
	}
	return token
}

// doREST issues a raw Forgejo REST request and returns the status code. It
// fails the test on any transport, marshal, or decode error rather than
// returning it, since a seed helper's failure is broken harness setup, not a
// condition under test. It is hand-rolled, independent of the forgejo
// package's own REST plumbing, for the AC3 cross-check below.
func doREST(t *testing.T, method, url, token string, body, out any) int {
	t.Helper()
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("forgejo harness: marshal %s %s body: %v", method, url, err)
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		t.Fatalf("forgejo harness: build %s %s: %v", method, url, err)
	}
	req.Header.Set("Authorization", "token "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := forgejoHTTPClient.Do(req)
	if err != nil {
		t.Fatalf("forgejo harness: %s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("forgejo harness: read %s %s response: %v", method, url, err)
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			t.Fatalf("forgejo harness: decode %s %s response: %v: %s", method, url, err, respBody)
		}
	}
	return resp.StatusCode
}

func require2xx(t *testing.T, status int, what string) {
	t.Helper()
	if status < 200 || status >= 300 {
		t.Fatalf("forgejo harness: %s: unexpected status %d", what, status)
	}
}

func seedRepo(t *testing.T, baseURL, token string) string {
	t.Helper()
	status := doREST(t, http.MethodPost, baseURL+"/api/v1/user/repos", token,
		map[string]any{"name": "harness", "auto_init": true, "default_branch": "main"}, nil)
	require2xx(t, status, "create repo")
	return forgejoAdminUser + "/harness"
}

// seedLabels prefixes each color with "#" because Forgejo's label-creation
// endpoint wants the leading "#", unlike forgejoHarnessLabels' bare-hex
// convention.
func seedLabels(t *testing.T, baseURL, token, repo string) {
	t.Helper()
	for _, l := range forgejoHarnessLabels() {
		status := doREST(t, http.MethodPost, baseURL+"/api/v1/repos/"+repo+"/labels", token,
			map[string]any{"name": l.Name, "color": "#" + l.Color}, nil)
		require2xx(t, status, fmt.Sprintf("create label %q", l.Name))
	}
}

// seedIssue applies labelNames through the same replace-labels endpoint the
// adapter's own setLabels uses.
func seedIssue(t *testing.T, baseURL, token, repo, title, body string, labelNames []string) int {
	t.Helper()
	var created struct {
		Number int `json:"number"`
	}
	status := doREST(t, http.MethodPost, baseURL+"/api/v1/repos/"+repo+"/issues", token,
		map[string]any{"title": title, "body": body}, &created)
	require2xx(t, status, fmt.Sprintf("create issue %q", title))
	if len(labelNames) > 0 {
		numStr := strconv.Itoa(created.Number)
		status := doREST(t, http.MethodPut, baseURL+"/api/v1/repos/"+repo+"/issues/"+numStr+"/labels", token,
			map[string]any{"labels": labelNames}, nil)
		require2xx(t, status, fmt.Sprintf("label issue %d", created.Number))
	}
	return created.Number
}

// seedBranchWithCommit creates branch off "main" with one trivial commit, the
// stand-in for an agent's real branch push, so the PR step below has something
// to open a pull request from.
func seedBranchWithCommit(t *testing.T, baseURL, token, repo, branch string) {
	t.Helper()
	status := doREST(t, http.MethodPost, baseURL+"/api/v1/repos/"+repo+"/contents/harness.txt", token,
		map[string]any{
			"content":    base64.StdEncoding.EncodeToString([]byte("hi")),
			"branch":     "main",
			"new_branch": branch,
			"message":    "harness work",
		}, nil)
	require2xx(t, status, fmt.Sprintf("seed branch %q with commit", branch))
}

// openPR returns the created PR's html_url, the same URL shape
// (".../pulls/<n>") every forge.PRForge method under test takes as input.
func openPR(t *testing.T, baseURL, token, repo, head, base, title string) string {
	t.Helper()
	var created struct {
		HTMLURL string `json:"html_url"`
	}
	status := doREST(t, http.MethodPost, baseURL+"/api/v1/repos/"+repo+"/pulls", token,
		map[string]any{"head": head, "base": base, "title": title}, &created)
	require2xx(t, status, fmt.Sprintf("open PR %s -> %s", head, base))
	if created.HTMLURL == "" {
		t.Fatalf("forgejo harness: open PR %s -> %s: response had no html_url", head, base)
	}
	return created.HTMLURL
}

// rawIssueLabels reads issue num's label names via a raw REST GET, decoded
// independently of the forgejo package's own forgejoIssuePayload struct. It is
// the AC3 cross-check oracle in TestForgejoIntegration_DispatchLifecycle.
func rawIssueLabels(t *testing.T, baseURL, token, repo, num string) []string {
	t.Helper()
	var payload struct {
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	status := doREST(t, http.MethodGet, baseURL+"/api/v1/repos/"+repo+"/issues/"+num, token, nil, &payload)
	if status != http.StatusOK {
		t.Fatalf("forgejo harness: raw GET issue %s: unexpected status %d", num, status)
	}
	names := make([]string, len(payload.Labels))
	for i, l := range payload.Labels {
		names[i] = l.Name
	}
	return names
}

func assertListed(t *testing.T, tr forge.IssueTracker, state forge.DispatchState, num string) {
	t.Helper()
	issues, err := tr.ListIssues(state)
	if err != nil {
		t.Fatalf("ListIssues(%v): %v", state, err)
	}
	for _, iss := range issues {
		if iss.Number == num {
			return
		}
	}
	t.Fatalf("ListIssues(%v): issue %s not found, want present", state, num)
}

func assertNotListed(t *testing.T, tr forge.IssueTracker, state forge.DispatchState, num string) {
	t.Helper()
	issues, err := tr.ListIssues(state)
	if err != nil {
		t.Fatalf("ListIssues(%v): %v", state, err)
	}
	for _, iss := range issues {
		if iss.Number == num {
			t.Fatalf("ListIssues(%v): issue %s unexpectedly present, want absent", state, num)
		}
	}
}

// TestForgejoIntegration_DispatchLifecycle boots a throwaway Forgejo from its
// OCI image and drives claim, work, PR, merge, complete through the forgejo
// adapters, so a green run exercises the real REST wire format rather than the
// httptest fake TestForgejoClient_TrackerContract runs against. It is opt-in
// behind the "integration" tag and self-skips where no daemon is reachable.
func TestForgejoIntegration_DispatchLifecycle(t *testing.T) {
	cli := requireForgejoRuntime(t)
	baseURL, token := bootForgejo(t, cli)

	repo := seedRepo(t, baseURL, token)
	seedLabels(t, baseURL, token, repo)

	tr := forgejo.NewForgejoClient(forgejo.ForgejoConfig{
		BaseURL:       baseURL,
		Repo:          repo,
		Token:         token,
		Labels:        testLabels,
		VerdictLabels: forge.ResearchVerdictLabels(),
	})
	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      baseURL,
		Repo:         repo,
		Token:        token,
		BaseBranch:   "main",
		UserName:     "Harness",
		UserEmail:    "harness@harness.local",
		BranchPrefix: "agent/issue-",
		MergeMethod:  "rebase",
	}, nil)
	prf, ok := cf.(forge.PRForge)
	if !ok {
		t.Fatalf("forgejo CodeForge does not implement forge.PRForge")
	}

	num := seedIssue(t, baseURL, token, repo, "harness lifecycle issue",
		"seeded by the forgejo integration harness", []string{testLabels.Dispatchable})
	numStr := strconv.Itoa(num)

	assertListed(t, tr, forge.Dispatchable, numStr)
	if err := tr.TransitionState(numStr, forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("TransitionState(claim): %v", err)
	}
	assertListed(t, tr, forge.InProgress, numStr)
	assertNotListed(t, tr, forge.Dispatchable, numStr)

	// AC3 cross-check: read the same issue through the adapter and through a
	// raw decode that shares neither forgejoIssuePayload's json tags nor
	// repoPath()'s URL routing. A renamed tag or changed path disagrees here
	// while contract_test.go's fake stays green, since a fake built to match
	// the adapter cannot catch the adapter drifting from the real wire format.
	iss, err := tr.Issue(numStr)
	if err != nil {
		t.Fatalf("Issue(%s): %v", numStr, err)
	}
	adapterLabels := append([]string(nil), iss.Labels...)
	rawLabels := rawIssueLabels(t, baseURL, token, repo, numStr)
	sort.Strings(adapterLabels)
	sort.Strings(rawLabels)
	if !slices.Equal(adapterLabels, rawLabels) {
		t.Fatalf("adapter-read labels %v disagree with raw REST oracle %v", adapterLabels, rawLabels)
	}

	branch := cf.AgentBranch(numStr)
	seedBranchWithCommit(t, baseURL, token, repo, branch)

	// OpenPRForBranch must find a PR that is still a draft under the
	// WIP-title convention, not just a ready one (issue #2408).
	prURL := openPR(t, baseURL, token, repo, branch, "main", "WIP: harness")
	if state, err := prf.PRState(prURL); err != nil {
		t.Fatalf("PRState (open): %v", err)
	} else if state != forge.PROpen {
		t.Fatalf("PRState (open) = %v, want %v", state, forge.PROpen)
	}
	if _, found, err := prf.OpenPRForBranch(branch); err != nil {
		t.Fatalf("OpenPRForBranch (draft): %v", err)
	} else if !found {
		t.Fatalf("OpenPRForBranch (draft): found=false, want true (a draft PR is adopted like any other)")
	}
	if err := prf.MarkReady(prURL); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if _, found, err := prf.OpenPRForBranch(branch); err != nil {
		t.Fatalf("OpenPRForBranch (ready): %v", err)
	} else if !found {
		t.Fatalf("OpenPRForBranch (ready): found=false, want true")
	}

	// Merge lives on forge.CodeForge, not PRForge: both github and forgejo
	// route a PR URL through it as ref.
	if err := cf.Merge(prURL); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if state, err := prf.PRState(prURL); err != nil {
		t.Fatalf("PRState (post-merge): %v", err)
	} else if state != forge.PRMerged {
		t.Fatalf("PRState (post-merge) = %v, want %v", state, forge.PRMerged)
	}

	if err := tr.TransitionState(numStr, forge.InProgress, forge.Complete); err != nil {
		t.Fatalf("TransitionState(complete): %v", err)
	}
	assertListed(t, tr, forge.Complete, numStr)
	assertNotListed(t, tr, forge.InProgress, numStr)
}
