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

// forgejoHarnessLabel is one label the integration harness seeds into a
// fresh Forgejo repo before driving the dispatch lifecycle against it.
// Color is a bare 6-hex-digit string with no leading "#" — the adapter's
// CreateLabel (forgejo.go:446) prepends the "#" itself.
type forgejoHarnessLabel struct {
	Name  string
	Color string
}

// forgejoHarnessLabels returns the full label set the harness seeds: the
// triage/dispatch family (testLabels, mirrored from lib/env-schema.nix) plus
// the research verdict family (forge.ResearchVerdictLabels) — both families
// a real dispatch run against Forgejo can exercise.
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

// forgejoContainerRunArgs builds the argv (without the leading cli) that
// launches a detached, throwaway Forgejo container for the harness: SQLite
// backend, install lock pre-set so first boot skips the setup wizard, and
// --rm so a crashed run doesn't leave a stopped container behind. When
// hostPort is 0 the publish flag omits the host side, letting the container
// runtime pick an ephemeral port (recovered later via parseForgejoHostPort).
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

// parseForgejoHostPort parses `docker port <name> 3000` / `podman port
// <name> 3000` output — one "<host>:<port>" mapping per line, e.g.
// "0.0.0.0:49153", with a possible ipv6 "[::]:49153" line alongside it — and
// returns the port from the first parseable line. Errors on empty or
// unparseable input rather than silently returning a zero port, since a
// harness that can't discover the published port has nothing to poll.
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
// bootstrapping the harness's admin user via `forgejo admin user create`.
// --must-change-password=false is required because the harness has no
// interactive terminal to satisfy a forced password change on first login.
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
// minting an all-scopes access token via `forgejo admin user
// generate-access-token`. --raw prints the bare token to stdout instead of a
// table, so the harness can capture it without parsing.
func forgejoTokenGenArgs(username string) []string {
	return []string{
		"forgejo", "admin", "user", "generate-access-token",
		"--username", username,
		"--scopes", "all",
		"--raw",
		"--token-name", "spindrift-harness",
	}
}

// forgejoVersionURL returns the version-endpoint URL the harness polls to
// detect that a freshly started Forgejo container is ready to accept
// requests, trimming any trailing slash on baseURL first so the join never
// doubles up a "/".
func forgejoVersionURL(baseURL string) string {
	return strings.TrimSuffix(baseURL, "/") + "/api/v1/version"
}

// requireForgejoRuntime returns the CLI name ("podman" or "docker") for the
// first runtime on PATH with a reachable daemon, skipping cleanly when
// neither is usable — mirroring internal/runner's requireRealOCI (issue
// #576's "skip on hosts with no real runtime"), reimplemented locally here
// since it's a small, self-contained probe and this package has no reason to
// import internal/runner.
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
// boots by default — a public image, so booting the harness never needs a
// registry credential. Overridable via SPINDRIFT_FORGEJO_IMAGE if this tag
// moves or a mirror is preferred.
const defaultForgejoHarnessImage = "codeberg.org/forgejo/forgejo:11"

// forgejoHarnessImage returns the Forgejo image the harness boots:
// SPINDRIFT_FORGEJO_IMAGE if set, else defaultForgejoHarnessImage.
func forgejoHarnessImage() string {
	if img := os.Getenv("SPINDRIFT_FORGEJO_IMAGE"); img != "" {
		return img
	}
	return defaultForgejoHarnessImage
}

// forgejoBootTimeout bounds launching the harness container, covering the
// implicit image pull on a cold cache.
const forgejoBootTimeout = 90 * time.Second

// forgejoPortTimeout bounds how long bootForgejo polls `<cli> port` for the
// runtime to have published the container's ephemeral host port.
const forgejoPortTimeout = 10 * time.Second

// forgejoReadyTimeout bounds how long bootForgejo polls the version endpoint
// for the freshly started container to start answering requests.
const forgejoReadyTimeout = 60 * time.Second

// forgejoAdminBootRetries bounds how many times bootForgejo retries the
// admin-user bootstrap command — the database may not be finished migrating
// the instant /version starts answering 200.
const forgejoAdminBootRetries = 5

// forgejoAdminUser, forgejoAdminPassword, and forgejoAdminEmail are the
// harness's throwaway admin credentials. They never leave the disposable,
// localhost-only container the harness boots and tears down within a single
// test.
const (
	forgejoAdminUser     = "root"
	forgejoAdminPassword = "spindrift-harness-pw"
	forgejoAdminEmail    = "root@harness.local"
)

// isForgejoRuntimeUnavailable reports whether output from a failed container
// launch indicates the registry or daemon is unreachable (a CI runner with no
// network egress, an unauthenticated pull quota, or a broken local daemon)
// rather than a genuine harness bug — the signal bootForgejo uses to skip
// instead of failing hard.
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

// bootForgejo launches a throwaway Forgejo container via cli, waits for it
// to become ready, bootstraps an admin user, and mints an access token for
// it, returning the instance's base URL and the minted token. It registers a
// t.Cleanup to remove the container as soon as the launch itself succeeds,
// so a later failure (readiness timeout, bootstrap failure) still tears the
// container down.
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

// forgejoWaitForPort polls `<cli> port <name> 3000` (parsed via
// parseForgejoHostPort) until the runtime reports the published host port,
// bounded by forgejoPortTimeout — the runtime may take a moment after
// `run -d` returns to have the mapping queryable.
func forgejoWaitForPort(t *testing.T, cli, name string) int {
	t.Helper()
	deadline := time.Now().Add(forgejoPortTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		out, err := exec.Command(cli, "port", name, "3000").Output()
		if err == nil {
			if port, perr := parseForgejoHostPort(string(out)); perr == nil {
				return port
			} else {
				lastErr = perr
			}
		} else {
			lastErr = err
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("forgejo harness: %s never published a host port for %s: %v", cli, name, lastErr)
	return 0
}

// forgejoWaitReady polls baseURL's version endpoint (forgejoVersionURL)
// until it answers HTTP 200, bounded by forgejoReadyTimeout.
func forgejoWaitReady(t *testing.T, baseURL string) {
	t.Helper()
	url := forgejoVersionURL(baseURL)
	deadline := time.Now().Add(forgejoReadyTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
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

// forgejoBootstrapAdmin runs `forgejo admin user create` (forgejoAdminCreateArgs)
// inside the harness container, retrying up to forgejoAdminBootRetries times
// since the database may not have finished migrating the instant the version
// endpoint starts answering 200. A "user already exists" failure (a retry
// racing its own prior, actually-successful attempt) is treated as success.
func forgejoBootstrapAdmin(t *testing.T, cli, name string) {
	t.Helper()
	args := append([]string{"exec", "-u", "git", name}, forgejoAdminCreateArgs(forgejoAdminUser, forgejoAdminPassword, forgejoAdminEmail)...)
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

// forgejoMintToken runs `forgejo admin user generate-access-token`
// (forgejoTokenGenArgs) inside the harness container and returns the raw
// token printed to stdout.
func forgejoMintToken(t *testing.T, cli, name string) string {
	t.Helper()
	args := append([]string{"exec", "-u", "git", name}, forgejoTokenGenArgs(forgejoAdminUser)...)
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

// doREST issues a raw Forgejo REST request against url with a
// "Authorization: token <token>" header, JSON-marshaling body (nil for none)
// and JSON-decoding the response into out (nil to discard it). It fails the
// test on any transport, marshal, or decode error rather than returning
// them, since every seed helper below treats such a failure as fatal
// harness setup, not a condition under test. It returns the HTTP status code
// so callers can assert on it themselves.
//
// This is deliberately a hand-rolled REST client, independent of the
// forgejo package's own forgejoClient.do — see the AC3 cross-check in
// TestForgejoIntegration_DispatchLifecycle for why that independence
// matters.
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
	resp, err := http.DefaultClient.Do(req)
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

// seedRepo creates the harness's single repository ("harness", owned by the
// admin user seeded by bootForgejo) with an initial commit on "main", and
// returns its owner/repo slug.
func seedRepo(t *testing.T, baseURL, token string) string {
	t.Helper()
	status := doREST(t, http.MethodPost, baseURL+"/api/v1/user/repos", token,
		map[string]any{"name": "harness", "auto_init": true, "default_branch": "main"}, nil)
	if status < 200 || status >= 300 {
		t.Fatalf("forgejo harness: create repo: unexpected status %d", status)
	}
	return forgejoAdminUser + "/harness"
}

// seedLabels creates every label forgejoHarnessLabels names on repo,
// prefixing each color with "#" — Forgejo's label-creation endpoint wants
// the leading "#", unlike forgejoHarnessLabels' own bare-hex convention.
func seedLabels(t *testing.T, baseURL, token, repo string) {
	t.Helper()
	for _, l := range forgejoHarnessLabels() {
		status := doREST(t, http.MethodPost, baseURL+"/api/v1/repos/"+repo+"/labels", token,
			map[string]any{"name": l.Name, "color": "#" + l.Color}, nil)
		if status < 200 || status >= 300 {
			t.Fatalf("forgejo harness: create label %q: unexpected status %d", l.Name, status)
		}
	}
}

// seedIssue creates an issue on repo with title/body and, if labelNames is
// non-empty, applies exactly those labels (by name, via the replace-labels
// endpoint the adapter's own setLabels also uses). It returns the created
// issue's number.
func seedIssue(t *testing.T, baseURL, token, repo, title, body string, labelNames []string) int {
	t.Helper()
	var created struct {
		Number int `json:"number"`
	}
	status := doREST(t, http.MethodPost, baseURL+"/api/v1/repos/"+repo+"/issues", token,
		map[string]any{"title": title, "body": body}, &created)
	if status < 200 || status >= 300 {
		t.Fatalf("forgejo harness: create issue %q: unexpected status %d", title, status)
	}
	if len(labelNames) > 0 {
		numStr := strconv.Itoa(created.Number)
		status := doREST(t, http.MethodPut, baseURL+"/api/v1/repos/"+repo+"/issues/"+numStr+"/labels", token,
			map[string]any{"labels": labelNames}, nil)
		if status < 200 || status >= 300 {
			t.Fatalf("forgejo harness: label issue %d: unexpected status %d", created.Number, status)
		}
	}
	return created.Number
}

// seedBranchWithCommit creates branch on repo off "main" carrying one new
// commit (a trivial harness.txt file) — the WORK step's stand-in for an
// agent's real branch push, giving the PR step below something to open a
// pull request from.
func seedBranchWithCommit(t *testing.T, baseURL, token, repo, branch string) {
	t.Helper()
	status := doREST(t, http.MethodPost, baseURL+"/api/v1/repos/"+repo+"/contents/harness.txt", token,
		map[string]any{
			"content":    base64.StdEncoding.EncodeToString([]byte("hi")),
			"branch":     "main",
			"new_branch": branch,
			"message":    "harness work",
		}, nil)
	if status < 200 || status >= 300 {
		t.Fatalf("forgejo harness: seed branch %q with commit: unexpected status %d", branch, status)
	}
}

// openPR opens a pull request on repo from head onto base with the given
// title and returns the created PR's html_url — the same URL shape
// (".../pulls/<n>") every forge.PRForge method on the adapter under test
// takes as input.
func openPR(t *testing.T, baseURL, token, repo, head, base, title string) string {
	t.Helper()
	var created struct {
		HTMLURL string `json:"html_url"`
	}
	status := doREST(t, http.MethodPost, baseURL+"/api/v1/repos/"+repo+"/pulls", token,
		map[string]any{"head": head, "base": base, "title": title}, &created)
	if status < 200 || status >= 300 {
		t.Fatalf("forgejo harness: open PR %s -> %s: unexpected status %d", head, base, status)
	}
	if created.HTMLURL == "" {
		t.Fatalf("forgejo harness: open PR %s -> %s: response had no html_url", head, base)
	}
	return created.HTMLURL
}

// rawIssueLabels reads issue num's label names via a raw REST GET, decoded
// independently of the forgejo package's own forgejoIssuePayload struct —
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

// assertListed fails the test if issue num is absent from
// tr.ListIssues(state).
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

// assertNotListed fails the test if issue num is present in
// tr.ListIssues(state).
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

// TestForgejoIntegration_DispatchLifecycle is the harness's end-to-end
// check: it boots a throwaway Forgejo instance from its OCI image (no
// external service, no credential beyond the throwaway admin account this
// test itself creates), seeds a repo/labels/issue, and drives the canonical
// dispatch loop — claim, work, PR, merge, complete — entirely through the
// forgejo package's own IssueTracker and CodeForge/PRForge adapters, so a
// green run exercises the real REST wire format, not the httptest fake
// contract_test.go's TestForgejoClient_TrackerContract runs against.
//
// This test is opt-in: it lives behind the "integration" build tag, so
// `go test ./...` never runs it, and it self-skips (via
// requireForgejoRuntime) wherever no container daemon is reachable —
// including this repo's own dogfood Box, which has none. Run it explicitly,
// pre-release or on demand:
//
//	go test -tags integration -run TestForgejoIntegration ./cmd/launcher/internal/forge/forgejo/
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
	})
	prf, ok := cf.(forge.PRForge)
	if !ok {
		t.Fatalf("forgejo CodeForge does not implement forge.PRForge")
	}

	num := seedIssue(t, baseURL, token, repo, "harness lifecycle issue",
		"seeded by the forgejo integration harness", []string{testLabels.Dispatchable})
	numStr := strconv.Itoa(num)

	// CLAIM: Dispatchable -> InProgress.
	assertListed(t, tr, forge.Dispatchable, numStr)
	if err := tr.TransitionState(numStr, forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("TransitionState(claim): %v", err)
	}
	assertListed(t, tr, forge.InProgress, numStr)
	assertNotListed(t, tr, forge.Dispatchable, numStr)

	// AC3 cross-check: the adapter's own Issue read against a raw REST read
	// of the same issue, decoded independently of forgejoIssuePayload's json
	// tags and independent of repoPath()'s URL routing. If either drifted —
	// a renamed json tag, a changed REST path — the two label sets would
	// disagree here even though contract_test.go's httptest fake (which
	// mirrors the adapter's own expectations back at it) would stay green,
	// since a fake built to match the adapter can't catch the adapter
	// itself drifting from the real wire format.
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

	// WORK: seed the agent branch the PR step opens a pull request from.
	branch := cf.AgentBranch(numStr)
	seedBranchWithCommit(t, baseURL, token, repo, branch)

	// PR: open as a draft (WIP-title convention), confirm it's never
	// adopted by OpenPRForBranch while draft, then mark it ready and
	// confirm it now is.
	prURL := openPR(t, baseURL, token, repo, branch, "main", "WIP: harness")
	if state, err := prf.PRState(prURL); err != nil {
		t.Fatalf("PRState (open): %v", err)
	} else if state != forge.PROpen {
		t.Fatalf("PRState (open) = %v, want %v", state, forge.PROpen)
	}
	if _, found, err := prf.OpenPRForBranch(branch); err != nil {
		t.Fatalf("OpenPRForBranch (draft): %v", err)
	} else if found {
		t.Fatalf("OpenPRForBranch (draft): found=true, want false (a draft PR is never adopted)")
	}
	if err := prf.MarkReady(prURL); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if _, found, err := prf.OpenPRForBranch(branch); err != nil {
		t.Fatalf("OpenPRForBranch (ready): %v", err)
	} else if !found {
		t.Fatalf("OpenPRForBranch (ready): found=false, want true")
	}

	// MERGE: Merge lives on the core forge.CodeForge surface, not PRForge —
	// both github and forgejo route a PR URL through it as ref.
	if err := cf.Merge(prURL); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if state, err := prf.PRState(prURL); err != nil {
		t.Fatalf("PRState (post-merge): %v", err)
	} else if state != forge.PRMerged {
		t.Fatalf("PRState (post-merge) = %v, want %v", state, forge.PRMerged)
	}

	// COMPLETE: InProgress -> Complete.
	if err := tr.TransitionState(numStr, forge.InProgress, forge.Complete); err != nil {
		t.Fatalf("TransitionState(complete): %v", err)
	}
	assertListed(t, tr, forge.Complete, numStr)
	assertNotListed(t, tr, forge.InProgress, numStr)
}
