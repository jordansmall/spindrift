package github

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// The primary-row contract runs: the production execClient under a primary
// github row, so its reads are the Connector's and its writes run gh. That gh
// is this test binary (see TestMain) doing what gh does for each write: it
// speaks the scripted REST backend's API, so a read and a write see one state.
// It serves only the commands the writes make, so a read that fell back to gh is
// refused, recorded, and fails the run.

const (
	fakeGHAPIEnv = "SPINDRIFT_TEST_FAKE_GH_API"
	fakeGHLogEnv = "SPINDRIFT_TEST_FAKE_GH_LOG"
)

var errFakeGHRefused = errors.New("command outside the writes a primary row leaves to gh")

type fakeGHCall struct {
	Args    []string `json:"args"`
	Refused bool     `json:"refused"`
}

func fakeGHMain(args []string) int {
	g := fakeGH{
		api: os.Getenv(fakeGHAPIEnv),
		client: &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // the scripted backend's self-signed cert
		}},
	}
	err := g.run(args)
	refused := errors.Is(err, errFakeGHRefused)
	if line, merr := json.Marshal(fakeGHCall{Args: args, Refused: refused}); merr == nil {
		if f, ferr := os.OpenFile(os.Getenv(fakeGHLogEnv), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); ferr == nil {
			_, _ = f.Write(append(line, '\n'))
			_ = f.Close()
		}
	}
	switch {
	case err == nil:
		return 0
	case refused:
		fmt.Fprintf(os.Stderr, "fake gh %v: %v\n", args, err)
		return 99
	}
	fmt.Fprintln(os.Stderr, "gh:", err)
	return 1
}

type fakeGH struct {
	api    string
	client *http.Client
}

func ghFlag(args []string, name string) string {
	if i := slices.Index(args, name); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func ghFlags(args []string, name string) (out []string) {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			out = append(out, args[i+1])
		}
	}
	return out
}

func (g fakeGH) run(args []string) error {
	if len(args) < 2 {
		return errFakeGHRefused
	}
	switch rest := args[2:]; args[0] + " " + args[1] {
	case "issue edit":
		return g.issueEdit(rest)
	case "pr merge":
		return g.prMerge(rest)
	case "pr ready":
		return g.prReady(rest)
	}
	return errFakeGHRefused
}

func (g fakeGH) do(method, path string, body any) ([]byte, error) {
	var in io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		in = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, g.api+"/"+strings.TrimPrefix(path, "/"), in)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+connectorToken)
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(out))
	}
	return out, nil
}

// graphql runs a mutation, failing on the errors array GitHub answers 200 with.
func (g fakeGH) graphql(query string, vars map[string]any) error {
	out, err := g.do(http.MethodPost, "graphql", map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	var resp struct{ Errors []struct{ Message string } }
	if json.Unmarshal(out, &resp) == nil && len(resp.Errors) > 0 {
		return errors.New(resp.Errors[0].Message)
	}
	return nil
}

func (g fakeGH) issueEdit(args []string) error {
	repo, num := ghFlag(args, "--repo"), args[0]
	if add := ghFlags(args, "--add-label"); len(add) > 0 {
		if _, err := g.do(http.MethodPost, "repos/"+repo+"/issues/"+num+"/labels", map[string]any{"labels": add}); err != nil {
			return err
		}
	}
	if remove := ghFlags(args, "--remove-label"); len(remove) > 0 {
		ids := make([]string, len(remove))
		for i, l := range remove {
			ids[i] = "LA_" + l
		}
		return g.graphql(`mutation($id:ID!,$ids:[ID!]!){removeLabelsFromLabelable(input:{labelableId:$id,labelIds:$ids}){clientMutationId}}`,
			map[string]any{"id": "I_" + num, "ids": ids})
	}
	return nil
}

// prPath splits a PR URL into its REST pull-request path and number.
func prPath(prURL string) (path, num string, err error) {
	u, err := url.Parse(prURL)
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if err != nil || len(parts) != 4 || parts[2] != "pull" {
		return "", "", fmt.Errorf("not a pull request URL: %q", prURL)
	}
	return "repos/" + parts[0] + "/" + parts[1] + "/pulls/" + parts[3], parts[3], nil
}

func (g fakeGH) pull(prURL string) (path, num string, p struct {
	Draft bool
	Head  struct{ Ref string }
	Base  struct{ Ref string }
}, err error) {
	path, num, err = prPath(prURL)
	if err != nil {
		return
	}
	out, err := g.do(http.MethodGet, path, nil)
	if err == nil {
		err = json.Unmarshal(out, &p)
	}
	return
}

func (g fakeGH) prMerge(args []string) error {
	path, num, err := prPath(args[0])
	if err != nil {
		return err
	}
	method := "rebase"
	for _, m := range []string{"merge", "squash"} {
		if slices.Contains(args, "--"+m) {
			method = m
		}
	}
	if slices.Contains(args, "--auto") {
		return g.graphql(`mutation($id:ID!){enablePullRequestAutoMerge(input:{pullRequestId:$id,mergeMethod:REBASE}){clientMutationId}}`,
			map[string]any{"id": nodeID(atoi(num))})
	}
	_, err = g.do(http.MethodPut, path+"/merge", map[string]any{"merge_method": method})
	return err
}

// prReady is gh pr ready: a PR already in the wanted state is a no-op.
func (g fakeGH) prReady(args []string) error {
	undo := slices.Contains(args, "--undo")
	_, num, p, err := g.pull(args[len(args)-1])
	if err != nil {
		return err
	}
	if p.Draft == undo {
		return nil
	}
	mutation := `mutation($id:ID!){markPullRequestReadyForReview(input:{pullRequestId:$id}){clientMutationId}}`
	if undo {
		mutation = `mutation($id:ID!){convertPullRequestToDraft(input:{pullRequestId:$id}){clientMutationId}}`
	}
	return g.graphql(mutation, map[string]any{"id": nodeID(atoi(num))})
}

// primaryContractEnv is a primary github row over the scripted backend with the
// fake gh first on PATH, and the production execClient to drive.
type primaryContractEnv struct {
	h      *connectorHarness
	c      *execClient
	logDir string
	ghLog  string
}

func newPrimaryContractEnv(t *testing.T) *primaryContractEnv {
	t.Helper()
	h, _, _, logDir := primaryHarness(t, useGithubRowPrimary)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(bin, "gh")); err != nil {
		t.Fatal(err)
	}
	e := &primaryContractEnv{
		h: h, logDir: logDir, ghLog: filepath.Join(t.TempDir(), "gh-calls.jsonl"),
		c: NewExecClient(connectorRepo, testLabels, WithVerdictLabels(forge.ResearchVerdictLabels())),
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv(fakeGHAPIEnv, h.backend.URL)
	t.Setenv(fakeGHLogEnv, e.ghLog)
	return e
}

// ghCalls is every gh invocation so far, as its first two arguments joined, and
// those the fake refused.
func (e *primaryContractEnv) ghCalls(t *testing.T) (verbs []string, refused [][]string) {
	t.Helper()
	raw, err := os.ReadFile(e.ghLog)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var c fakeGHCall
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("gh call log line %q: %v", line, err)
		}
		verbs = append(verbs, strings.Join(c.Args[:min(2, len(c.Args))], " "))
		if c.Refused {
			refused = append(refused, c.Args)
		}
	}
	return verbs, refused
}

// assertSplit is the run's proof that reads and writes took the two paths: gh
// refused nothing and made each wanted write, the Connector sent each wanted
// read and none was refused, the backend saw nothing outside its API, and the
// primary log holds exactly the wantFailed failures the contract injected.
func (e *primaryContractEnv) assertSplit(t *testing.T, wantFailed int, wantGH, wantReads []string) {
	t.Helper()
	verbs, refused := e.ghCalls(t)
	if len(refused) > 0 {
		t.Errorf("gh was asked for reads a primary row must route to the Connector: %q", refused)
	}
	for _, w := range wantGH {
		if !slices.ContainsFunc(verbs, func(v string) bool { return strings.HasPrefix(v, w) }) {
			t.Errorf("gh never ran %q; ran %q", w, verbs)
		}
	}

	audits := primaryFiles(t, e.logDir, ".audit.jsonl")
	if len(audits) != 1 {
		t.Fatalf("primary audit files = %v, want one", audits)
	}
	raw, err := os.ReadFile(audits[0])
	if err != nil {
		t.Fatal(err)
	}
	var sent []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var a connectorhost.AuditEntry
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			t.Fatalf("audit line %q: %v", line, err)
		}
		if a.Decision != connectorhost.DecisionSent {
			t.Errorf("Connector request %s %s was %s: %s", a.Method, a.Path, a.Decision, a.Reason)
		}
		sent = append(sent, a.Method+" "+a.Path)
	}
	for _, w := range wantReads {
		if !slices.ContainsFunc(sent, func(k string) bool { return strings.HasPrefix(k, w) }) {
			t.Errorf("the Connector sent no %q; sent %q", w, slices.Compact(slices.Sorted(slices.Values(sent))))
		}
	}
	failed := 0
	for _, log := range primaryFiles(t, e.logDir, ".log") {
		raw, _ := os.ReadFile(log)
		failed += strings.Count(string(raw), "\n")
	}
	if failed != wantFailed {
		t.Errorf("primary log holds %d failed reads, want %d", failed, wantFailed)
	}
	e.h.backend.mu.Lock()
	unexpected := slices.Clone(e.h.backend.unexpected)
	e.h.backend.mu.Unlock()
	if len(unexpected) > 0 {
		t.Errorf("backend saw requests outside the API: %q", unexpected)
	}
}

// primaryTrackerHarness is the tracker contract's harness over a primary row.
type primaryTrackerHarness struct{ *primaryContractEnv }

var (
	_ forgetest.Harness                 = primaryTrackerHarness{}
	_ forgetest.NativeCapable           = primaryTrackerHarness{}
	_ forgetest.NativeFailureIsolatable = primaryTrackerHarness{}
	_ forgetest.PriorityCapable         = primaryTrackerHarness{}
	_ forgetest.CommentCapable          = primaryTrackerHarness{}
)

func (p primaryTrackerHarness) Tracker() forge.IssueTracker              { return p.c }
func (p primaryTrackerHarness) SeedIssue(i forge.Issue)                  { p.h.SeedIssue(i) }
func (p primaryTrackerHarness) FailNativeDeps(n string)                  { p.h.FailNativeDeps(n) }
func (p primaryTrackerHarness) SeedNativeDeps(n string, ids []string)    { p.h.SeedNativeDeps(n, ids) }
func (p primaryTrackerHarness) SeedComments(n string, c []forge.Comment) { p.h.SeedComments(n, c) }
func (primaryTrackerHarness) IsolatesNativeFailure()                     {}
func (primaryTrackerHarness) IsPriorityCapable()                         {}

// With a primary github row the tracker contract passes: every read and every
// issue write (claim, transition, verdict, comment) is the Connector's, and gh
// runs nothing.
func TestPrimaryRow_TrackerContract(t *testing.T) {
	e := newPrimaryContractEnv(t)

	forgetest.RunTrackerContract(t, primaryTrackerHarness{e})

	// Two failures are recorded: the injected native-deps read, and the verdict
	// the contract settles on an issue that is no longer in progress. The lost
	// claim race it also drives is core policy and stays out of the log.
	e.assertSplit(t, 2,
		nil,
		[]string{"GET /repos/" + connectorRepo + "/issues", "GET /repos/" + connectorRepo + "/issues/302/dependencies/blocked_by", "POST /graphql", "POST /repos/" + connectorRepo + "/issues"})
	if verbs, _ := e.ghCalls(t); len(verbs) > 0 {
		t.Errorf("the tracker contract ran gh %q under a primary row", verbs)
	}
}

// primaryPRHarness is the PRForge contract's harness over a primary row: the
// seeds and oracles are the scripted backend's, the PRForge under test the
// execClient.
type primaryPRHarness struct {
	composedPRHarness
	c *execClient
}

func (p primaryPRHarness) Forge() forge.PRForge       { return p.c }
func (p primaryPRHarness) CodeForge() forge.CodeForge { return p.c }

// With a primary github row the PRForge contract passes: PR and check reads are
// the Connector's, and merge, ready, draft and auto-merge are gh's.
func TestPrimaryRow_PRForgeContract(t *testing.T) {
	e := newPrimaryContractEnv(t)

	forgetest.RunPRForgeContract(t, primaryPRHarness{composedPRHarness{e.h}, e.c})

	e.assertSplit(t, 0,
		[]string{"pr merge", "pr ready"},
		[]string{"GET /repos/" + connectorRepo + "/pulls", "GET /repos/" + connectorRepo + "/compare/", "POST /graphql"})
}
