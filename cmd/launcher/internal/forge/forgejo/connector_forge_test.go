package forgejo_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorrecord"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/hostpaths"
)

// connectorCredentialEnv is the one credential a forgejo run holds: the Connector
// and core's git both read it.
const connectorCredentialEnv = forgejo.ForgejoTokenEnv

// useForgejoRow applies a forgejo backend row, shaped as the input document
// carries it, with its {base} routes resolved against baseURL.
func useForgejoRow(t *testing.T, baseURL string, adjust func(*backendrows.Row)) {
	t.Helper()
	path, table := declaredRoutes(t)
	row := backendrows.Row{
		Name: "forgejo", Module: path, Routes: table,
		CredentialEnv: connectorCredentialEnv, BaseEnv: forgejo.ForgejoBaseURLEnv, GitRemoteEnv: forgejo.ForgejoBaseURLEnv,
	}
	adjust(&row)
	doc, err := json.Marshal([]backendrows.Row{row})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backendrows.Snapshot())
	if err := backendrows.Use(doc, func(k string) string {
		if k == forgejo.ForgejoBaseURLEnv {
			return baseURL
		}
		return ""
	}); err != nil {
		t.Fatal(err)
	}
}

// buildForge runs the production constructor; git and the Connector both read
// FORGEJO_TOKEN from the environment.
func buildForge(srv *httptest.Server, mergeMethod string) (forge.CodeForge, error) {
	return buildForgeAt(srv, srv.URL, mergeMethod)
}

// buildForgeAt is buildForge for an instance served under base, which may carry a subpath.
func buildForgeAt(srv *httptest.Server, base, mergeMethod string) (forge.CodeForge, error) {
	l := mapLookup(map[string]string{forgejo.ForgejoBaseURLEnv: base, forgejo.ForgejoTokenEnv: connectorToken})
	s := backendkit.Shared{RepoSlug: connectorRepo, BaseBranch: "main", GitUserName: "Test Bot", GitUserEmail: "bot@example.com", MergeMethod: mergeMethod}
	return forgejo.Strategy.NewCodeForge(s, l, forge.SanitizedParent{}, nil)
}

// productionForge builds the Code Forge the way the launcher does under a
// forgejo row pointing at the compiled Connector and srv, returning the
// resolved Route table and the Connector's audit file.
func productionForge(t *testing.T, srv *httptest.Server) (forge.CodeForge, []connectorhost.Route, string, error) {
	t.Helper()
	return productionForgeAt(t, srv, srv.URL)
}

// productionForgeAt is productionForge for an instance served under base.
func productionForgeAt(t *testing.T, srv *httptest.Server, base string) (forge.CodeForge, []connectorhost.Route, string, error) {
	t.Helper()
	useForgejoRow(t, base, func(*backendrows.Row) {})
	t.Setenv(connectorCredentialEnv, connectorToken)
	t.Cleanup(forgejo.SetConnectorClient(srv.Client()))
	dir := t.TempDir()
	t.Chdir(dir)
	cf, err := buildForgeAt(srv, base, "")
	row, _ := backendrows.Lookup("forgejo")
	return cf, connectortracker.Routes(row.Routes, connectorRepo), filepath.Join(hostpaths.LogDir(dir), connectorrecord.Name("forgejo", ".audit.jsonl")), err
}

// A Connector that fails to load fails the construction with its error and
// builds no forge, native or otherwise.
func TestConnectorForge_LoadFailureFailsConstruction(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	useForgejoRow(t, srv.URL, func(r *backendrows.Row) { r.Module = filepath.Join(t.TempDir(), "absent.wasm") })
	t.Chdir(t.TempDir())

	cf, err := buildForge(srv, "")

	if err == nil || !strings.Contains(err.Error(), "read module") {
		t.Errorf("err = %v, want the module read error", err)
	}
	if cf != nil {
		t.Errorf("built a %T despite the load failure", cf)
	}
}

// A Connector never runs unrecorded: an audit file that will not open fails the
// construction with its path, and nothing reaches the instance.
func TestConnectorForge_UnopenableAuditFileFailsConstruction(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)
	useForgejoRow(t, srv.URL, func(*backendrows.Row) {})
	t.Setenv(connectorCredentialEnv, connectorToken)
	t.Cleanup(forgejo.SetConnectorClient(srv.Client()))
	dir := t.TempDir()
	t.Chdir(dir)
	logDir := hostpaths.LogDir(dir)
	if err := os.MkdirAll(filepath.Dir(logDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logDir, nil, 0o644); err != nil { // a file where the log directory belongs
		t.Fatal(err)
	}

	cf, err := buildForge(srv, "")

	want := filepath.Join(logDir, connectorrecord.Name("forgejo", ".audit.jsonl"))
	if err == nil || !strings.Contains(err.Error(), want) || cf != nil {
		t.Errorf("buildForge = (%v, %v), want (nil, an error naming %s)", cf, err, want)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("instance saw %d requests, want none", n)
	}
}

func TestConnectorForge_LoadErrorIsReturnedAsIs(t *testing.T) {
	boom := errors.New("connector would not load")
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	useForgejoRow(t, srv.URL, func(*backendrows.Row) {})
	t.Cleanup(forgejo.SetLoadRowConnector(func(context.Context, backendrows.Row, backendrows.Options) (*connectorhost.Connector, error) {
		return nil, boom
	}))
	t.Chdir(t.TempDir())

	if cf, err := buildForge(srv, ""); !errors.Is(err, boom) || cf != nil {
		t.Errorf("buildForge = (%v, %v), want (nil, the load error)", cf, err)
	}
}

// A request outside the row's Routes is refused by the host: the operation
// fails with that refusal, and the forge is the Connector's, never a native
// fallback that would have answered.
func TestConnectorForge_RefusedRouteFailsTheOperation(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	useForgejoRow(t, srv.URL, func(r *backendrows.Row) {
		r.Routes = append(slicesDeleteMethod(r.Routes, "GET"), connectorhost.Route{Method: "GET", Host: backendrows.BaseHost, Path: "/unrelated"})
	})
	t.Setenv(connectorCredentialEnv, connectorToken)
	t.Cleanup(forgejo.SetConnectorClient(srv.Client()))
	t.Chdir(t.TempDir())

	cf, err := buildForge(srv, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = cf.Probe()

	if !errors.Is(err, connectorhost.ErrCall) || !errors.Is(err, connectorhost.ErrRouteRefused) {
		t.Errorf("Probe = %v, want an error wrapping connectorhost.ErrCall and ErrRouteRefused", err)
	}
}

func slicesDeleteMethod(routes []connectorhost.Route, method string) []connectorhost.Route {
	var out []connectorhost.Route
	for _, r := range routes {
		if r.Method != method {
			out = append(out, r)
		}
	}
	return out
}

// The Connector merges by rebase only, so a MERGE_METHOD it cannot honour is
// refused rather than silently ignored.
func TestConnectorForge_RefusesAMergeMethodTheConnectorCannotHonour(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	useForgejoRow(t, srv.URL, func(*backendrows.Row) {})
	for _, method := range []string{"squash", "merge"} {
		cf, err := buildForge(srv, method)
		if !errors.Is(err, forgejo.ErrMergeMethod) || !strings.Contains(err.Error(), "MERGE_METHOD") || cf != nil {
			t.Errorf("MERGE_METHOD=%s: buildForge = (%v, %v), want (nil, ErrMergeMethod)", method, cf, err)
		}
	}
}

// A plain row builds both halves from the Connector.
func TestPlainRow_BuildsTheConnectorForgeAndTracker(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	t.Setenv(connectorCredentialEnv, connectorToken)
	t.Cleanup(forgejo.SetConnectorClient(srv.Client()))
	t.Chdir(t.TempDir())

	useForgejoRow(t, srv.URL, func(*backendrows.Row) {})
	if _, err := buildForge(srv, ""); err != nil {
		t.Fatal(err)
	}
	tr, err := buildTracker(srv)
	if err != nil {
		t.Fatal(err)
	}
	if got := reflect.TypeOf(tr).String(); !strings.Contains(got, "connectortracker") {
		t.Errorf("built %s, want the Connector tracker", got)
	}
}

// Forgejo runs only through its Connector: without a row neither the Code
// Forge nor the tracker is built, and neither degrades to a native adapter.
func TestNoConnectorRow_RefusesTheCodeForgeAndTheTracker(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	t.Cleanup(forgejo.SetLoadRowConnector(func(context.Context, backendrows.Row, backendrows.Options) (*connectorhost.Connector, error) {
		t.Error("a Connector loaded without a usable row")
		return nil, errors.New("unexpected load")
	}))
	cf, err := buildForge(srv, "squash")
	if !errors.Is(err, forgejo.ErrNoConnectorRow) || cf != nil {
		t.Errorf("buildForge = (%v, %v), want (nil, ErrNoConnectorRow)", cf, err)
	}
	tr, err := buildTracker(srv)
	if !errors.Is(err, forgejo.ErrNoConnectorRow) || tr != nil {
		t.Errorf("buildTracker = (%v, %v), want (nil, ErrNoConnectorRow)", tr, err)
	}
}

// A plain row whose base is not https is dropped by the row set as unusable;
// the forge build must name the base, not point at a row that exists.
func TestPlainRow_PlaintextBaseNamesTheBase(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	useForgejoRow(t, "http://forgejo.lan:3000", func(*backendrows.Row) {})

	cf, err := buildForge(srv, "squash")
	if !errors.Is(err, forgejo.ErrNoConnectorRow) || cf != nil {
		t.Fatalf("buildForge = (%v, %v), want (nil, ErrNoConnectorRow)", cf, err)
	}
	if !strings.Contains(err.Error(), "must be an https URL") || !strings.Contains(err.Error(), forgejo.ForgejoBaseURLEnv) {
		t.Errorf("err = %q, want it to say %s must be an https URL", err, forgejo.ForgejoBaseURLEnv)
	}
	tr, err := buildTracker(srv)
	if !errors.Is(err, forgejo.ErrNoConnectorRow) || tr != nil || !strings.Contains(err.Error(), "must be an https URL") {
		t.Errorf("buildTracker = (%v, %v), want (nil, ErrNoConnectorRow naming the base)", tr, err)
	}
}

// The production forgejo forge, composed under a forgejo row, exposes the
// head-pinned rebase to the Lander, refuses a PR whose guest-named head is not
// the derived agent branch, and refuses a plain Rebase, each before any branch
// is pushed (issue #6961). A matching head rebases.
func TestConnectorForge_RebasePRHeadIsPinnedToTheAgentBranch(t *testing.T) {
	h := newConnectorHarness(t, true)
	rebaser := h.caps.PRHeadRebaser
	if rebaser == nil {
		t.Fatal("ResolveCodeForge found no PRHeadRebaser on the production forgejo forge")
	}
	url := h.seedLandable("21")
	h.repo.SeedBranch("release", "rel")
	h.repo.AdvanceBase()
	branch := forge.AgentBranchName(h.BranchPrefix(), "21")
	before := h.repo.BranchSHA(branch)

	for name, err := range map[string]error{
		"foreign head": rebaser.RebasePRHead(url, "release"),
		"other agent":  rebaser.RebasePRHead(url, forge.AgentBranchName(h.BranchPrefix(), "999")),
		"plain rebase": h.cf.Rebase(url),
	} {
		if !errors.Is(err, connectorforge.ErrAnswer) {
			t.Errorf("%s: err = %v, want connectorforge.ErrAnswer", name, err)
		}
	}
	if got := h.repo.BranchSHA(branch); got != before || h.repo.Rebased(branch) {
		t.Error("a refused rebase moved the PR branch")
	}
	if h.repo.Rebased("release") {
		t.Error("the foreign branch was rebased")
	}
	if err := rebaser.RebasePRHead(url, branch); err != nil {
		t.Fatalf("RebasePRHead(matching head) = %v", err)
	}
	if !h.repo.Rebased(branch) {
		t.Error("the matching head was not rebased")
	}
}

// A self-hosted forgejo under a subpath: the production constructor reads the
// PR the instance answers under the base's path, its html_url keeps that path,
// and the composed forge accepts the URL it handed back.
func TestConnectorForge_SelfHostedUnderASubpath(t *testing.T) {
	h := newConnectorHarnessAt(t, true, "/forgejo")
	want := h.srv.URL + "/forgejo/" + connectorRepo + "/pulls/21"
	if got := h.seedLandable("21"); got != want {
		t.Fatalf("seeded html_url = %q, want %q", got, want)
	}

	pr, ok, err := h.caps.PRForge.OpenPRForBranch(forge.AgentBranchName(h.BranchPrefix(), "21"))
	if err != nil || !ok || pr.URL != want {
		t.Fatalf("OpenPRForBranch = (%+v, %v, %v), want the PR at %s", pr, ok, err, want)
	}
	if err := h.cf.Merge(pr.URL); err != nil {
		t.Fatalf("Merge(%s) = %v", pr.URL, err)
	}
	if !h.merged(pr.URL) {
		t.Error("the PR was not merged")
	}
}

// buildTracker runs the production constructor for the issue tracker.
func buildTracker(srv *httptest.Server) (forge.IssueTracker, error) {
	l := mapLookup(map[string]string{forgejo.ForgejoBaseURLEnv: srv.URL, forgejo.ForgejoTokenEnv: connectorToken})
	return forgejo.Strategy.NewIssueTracker(backendkit.Shared{RepoSlug: connectorRepo, Labels: testLabels, VerdictLabels: forge.ResearchVerdictLabels()}, l)
}

// A Connector that fails to load fails the tracker's construction with its
// error and builds no tracker, native or otherwise.
func TestConnectorTracker_LoadFailureFailsConstruction(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	useForgejoRow(t, srv.URL, func(r *backendrows.Row) { r.Module = filepath.Join(t.TempDir(), "absent.wasm") })
	t.Chdir(t.TempDir())

	tr, err := buildTracker(srv)

	if err == nil || !strings.Contains(err.Error(), "read module") || tr != nil {
		t.Errorf("buildTracker = (%v, %v), want (nil, the module read error)", tr, err)
	}
}

func TestConnectorTracker_LoadErrorIsReturnedAsIs(t *testing.T) {
	boom := errors.New("connector would not load")
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	useForgejoRow(t, srv.URL, func(*backendrows.Row) {})
	t.Cleanup(forgejo.SetLoadRowConnector(func(context.Context, backendrows.Row, backendrows.Options) (*connectorhost.Connector, error) {
		return nil, boom
	}))
	t.Chdir(t.TempDir())

	if tr, err := buildTracker(srv); !errors.Is(err, boom) || tr != nil {
		t.Errorf("buildTracker = (%v, %v), want (nil, the load error)", tr, err)
	}
}

// A Connector whose handshake omits a capability the tracker needs would leave
// that operation with nothing behind it, so loading it fails by name and
// closes it.
func TestConnectorTracker_RefusesAConnectorMissingACapability(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	useForgejoRow(t, srv.URL, func(*backendrows.Row) {})
	var loaded *connectorhost.Connector
	t.Cleanup(forgejo.SetLoadRowConnector(func(ctx context.Context, row backendrows.Row, o backendrows.Options) (*connectorhost.Connector, error) {
		c, err := backendrows.Load(ctx, row, o)
		if err == nil {
			c.Capabilities = slices.DeleteFunc(slices.Clone(c.Capabilities), func(cap string) bool {
				return cap == connectortracker.CapLabeled || cap == connectortracker.CapProbe
			})
			loaded = c
		}
		return c, err
	}))
	t.Setenv(connectorCredentialEnv, connectorToken)
	t.Cleanup(forgejo.SetConnectorClient(srv.Client()))
	t.Chdir(t.TempDir())

	tr, err := buildTracker(srv)

	if err == nil || !strings.Contains(err.Error(), "forgejo Connector lacks required capabilities: tracker.probe, tracker.labeled") || tr != nil {
		t.Errorf("buildTracker = (%v, %v), want (nil, the missing capabilities named)", tr, err)
	}
	if loaded == nil || !errors.Is(loaded.Call(context.Background(), connectortracker.CapProbe, struct{}{}, &struct{}{}), connectorhost.ErrUnusable) {
		t.Error("the refused Connector was left open")
	}
}

// A request outside the row's Routes is refused by the host: the tracker
// operation fails with that refusal, and nothing native answers in its place.
func TestConnectorTracker_RefusedRouteFailsTheOperation(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	t.Cleanup(srv.Close)
	useForgejoRow(t, srv.URL, func(r *backendrows.Row) {
		r.Routes = append(slicesDeleteMethod(r.Routes, "GET"), connectorhost.Route{Method: "GET", Host: backendrows.BaseHost, Path: "/unrelated"})
	})
	t.Setenv(connectorCredentialEnv, connectorToken)
	t.Cleanup(forgejo.SetConnectorClient(srv.Client()))
	t.Chdir(t.TempDir())

	tr, err := buildTracker(srv)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tr.Issue("1")

	if !errors.Is(err, connectorhost.ErrCall) || !errors.Is(err, connectorhost.ErrRouteRefused) {
		t.Errorf("Issue = %v, want an error wrapping connectorhost.ErrCall and ErrRouteRefused", err)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("the backend saw %d requests, want none", n)
	}
}

// The Connector-only tracker exposes every optional capability below, so no
// caller's forge.As lookup silently misses.
func TestConnectorTracker_ExposesTheNativeCapabilitySet(t *testing.T) {
	w := newTrackerHarness(t)
	for name, ok := range map[string]bool{
		"NativeDepsReader":     asOK[forge.NativeDepsReader](w.tr),
		"BlockersLister":       asOK[forge.BlockersLister](w.tr),
		"CommentLister":        asOK[forge.CommentLister](w.tr),
		"HostPostedCommenter":  asOK[forge.HostPostedCommenter](w.tr),
		"HostPostedIssueFiler": asOK[forge.HostPostedIssueFiler](w.tr),
		"IssueLabeler":         asOK[forge.IssueLabeler](w.tr),
		"MergeCloser":          asOK[forge.MergeCloser](w.tr),
		"DemandCounter":        asOK[forge.DemandCounter](w.tr),
		"LabeledBacklogLister": asOK[forge.LabeledBacklogLister](w.tr),
		"IssueSearcher":        asOK[forge.IssueSearcher](w.tr),
		"LabeledTracker":       asOK[forge.LabeledTracker](w.tr),
		"FullyPaginated":       asOK[forge.FullyPaginated](w.tr),
	} {
		if !ok {
			t.Errorf("the Connector-only tracker lacks %s", name)
		}
	}
	// The Connector's list is one capped page.
	if fp, _ := forge.As[forge.FullyPaginated](w.tr); fp == nil || fp.WalksAllPages() {
		t.Error("FullyPaginated.WalksAllPages = true, want false")
	}
}

func asOK[T any](it forge.IssueTracker) bool {
	_, ok := forge.As[T](it)
	return ok
}
