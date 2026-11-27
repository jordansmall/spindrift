package github

import (
	"context"
	"errors"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
	"spindrift.dev/launcher/internal/credsource/credsourcetest"
	"spindrift.dev/launcher/internal/forge"
)

// loadRecorder is the in-process engine a runtime owns in a test, remembering
// every Connector it loaded.
type loadRecorder struct {
	connectorsandbox.Loader
	mu     sync.Mutex
	loaded []*connectorhost.Connector
}

func (r *loadRecorder) LoadNetwork(ctx context.Context, wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error) {
	c, err := r.Loader.LoadNetwork(ctx, wasm, n)
	if err == nil {
		r.mu.Lock()
		r.loaded = append(r.loaded, c)
		r.mu.Unlock()
	}
	return c, err
}

func (r *loadRecorder) loads() []*connectorhost.Connector {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*connectorhost.Connector(nil), r.loaded...)
}

// recordingRuntime is a runtime over the applied github row whose own sandbox
// is a loadRecorder, closed with the test.
func recordingRuntime(t *testing.T) (*connectorruntime.Runtime, *loadRecorder) {
	t.Helper()
	row, ok := backendrows.Lookup("github")
	if !ok {
		t.Fatal("no github backend row applied")
	}
	rec := &loadRecorder{Loader: connectorsandboxtest.InProcess()}
	rt := connectorruntime.New(backendrows.NewSet(row), credsourcetest.EnvRows(row), rec)
	t.Cleanup(func() { _ = rt.Close() })
	return rt, rec
}

// clientViews is what an execClient keeps across operations.
type clientViews struct {
	tracker  forge.IssueTracker
	reader   forge.CodeForge
	composed forge.CodeForge
	git      *coreGit
}

func viewsOf(e *execClient) clientViews {
	e.mu.Lock()
	defer e.mu.Unlock()
	return clientViews{e.tracker, e.reader.cf, e.composed.cf, e.composed.git}
}

// A tracker and Code Forge built once and run through many operations build
// their views once and share one session: one Connector load, none rebuilt.
func TestConnector_OperationsShareOneSession(t *testing.T) {
	h, _, _, _ := routedLogHarness(t, useGithubRowAt)
	h.SeedIssue(forge.Issue{Number: "1", Title: "one", Labels: []string{testLabels.Dispatchable}})
	prURL := h.SeedDraftPR("9")
	h.backend.repo.SeedBranch(landingBranch, "9")
	rt, rec := recordingRuntime(t)
	shared := backendkit.Shared{RepoSlug: connectorRepo, Labels: testLabels, Connectors: rt}
	tr, err := Strategy.NewIssueTracker(shared, nil)
	if err != nil {
		t.Fatal(err)
	}
	cf, err := Strategy.NewCodeForge(shared, nil, forge.SanitizedParent{}, tr)
	if err != nil {
		t.Fatal(err)
	}
	trClient, cfClient := tr.(*execClient), cf.(*execClient)
	if n := len(rec.loads()); n != 0 {
		t.Fatalf("building the tracker and Code Forge loaded %d Connectors, want none", n)
	}

	steps := []struct {
		name   string
		client *execClient
		do     func() error
	}{
		{"Issue", trClient, func() error { _, err := tr.Issue("1"); return err }},
		{"ListIssues", trClient, func() error { _, err := tr.ListIssues(forge.Dispatchable); return err }},
		{"CountReady", trClient, func() error { _, err := trClient.CountReady(false); return err }},
		{"BranchExists", cfClient, func() error { _, err := cf.BranchExists(landingBranch); return err }},
		{"PRState", cfClient, func() error { _, err := cfClient.PRState(prURL); return err }},
		{"MarkReady", cfClient, func() error { return cfClient.MarkReady(prURL) }},
		{"MarkDraft", cfClient, func() error { return cfClient.MarkDraft(prURL) }},
	}
	var first [2]clientViews
	for round := 0; round < 3; round++ {
		for _, s := range steps {
			if err := s.do(); err != nil {
				t.Fatalf("round %d: %s: %v", round, s.name, err)
			}
			if round == 2 {
				if got := [2]clientViews{viewsOf(trClient), viewsOf(cfClient)}; got != first {
					t.Fatalf("%s rebuilt a view: %+v, want %+v", s.name, got, first)
				}
			}
		}
		if round == 0 {
			first = [2]clientViews{viewsOf(trClient), viewsOf(cfClient)}
			if first[0].tracker == nil || first[1].reader == nil || first[1].composed == nil || first[1].git == nil {
				t.Fatalf("a view was never built: %+v", first)
			}
		}
	}
	if n := len(rec.loads()); n != 1 {
		t.Errorf("%d Connector loads across every operation, want one", n)
	}
}

// A guest killed by a trap fails the call that finds it dead; the next read
// reloads in the runtime's own sandbox and succeeds on the views already built.
func TestConnector_KilledGuestReloadsInsideTheRuntimeSandbox(t *testing.T) {
	h, _, _, _ := routedLogHarness(t, useGithubRowAt)
	h.SeedIssue(forge.Issue{Number: "2", Title: "two"})
	rt, rec := recordingRuntime(t)
	c := NewExecClient(connectorRepo, testLabels, WithConnectors(rt))
	if _, err := c.Issue("2"); err != nil {
		t.Fatalf("first read: %v", err)
	}
	loads := rec.loads()
	if len(loads) != 1 {
		t.Fatalf("%d loads after the first read, want one", len(loads))
	}
	views := viewsOf(c)

	loads[0].Abort()

	if _, err := c.Issue("2"); !errors.Is(err, connectorhost.ErrTrap) {
		t.Fatalf("read on the killed guest = %v, want ErrTrap", err)
	}
	if got, err := c.Issue("2"); err != nil || got.Title != "two" {
		t.Fatalf("read after the kill = %+v, %v; want issue two", got, err)
	}
	loads = rec.loads()
	if len(loads) != 2 {
		t.Fatalf("%d loads after the reload, want two", len(loads))
	}
	if loads[1].Err() != nil {
		t.Errorf("the reloaded Connector is dead: %v", loads[1].Err())
	}
	if viewsOf(c) != views {
		t.Error("the reload rebuilt a view")
	}
}
