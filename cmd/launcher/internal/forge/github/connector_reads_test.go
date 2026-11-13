package github

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
)

// routedHarness configures a github backend row at the scripted backend and
// puts a gh on PATH that fails every call, so a read that reaches gh shows in
// calls. The returned client is the production execClient.
func routedHarness(t *testing.T) (h *connectorHarness, c *execClient, calls func() int) {
	t.Helper()
	return harnessWithRow(t, useGithubRowAt)
}

// harnessWithRow is routedHarness with the github row applied by useRow.
func harnessWithRow(t *testing.T, useRow func(t *testing.T, host string)) (h *connectorHarness, c *execClient, calls func() int) {
	t.Helper()
	h = newConnectorHarness(t)
	useRow(t, h.backend.host)
	old := connectorClient
	connectorClient = h.backend.Client()
	t.Cleanup(func() { connectorClient = old })
	t.Setenv(tokenCredentialEnv, connectorToken)
	t.Cleanup(closeSharedConnectors)
	calls = countGH(t)
	return h, NewExecClient(connectorRepo, testLabels), calls
}

// withoutCapability makes the row's Connector load as one whose handshake lacks
// capName.
func withoutCapability(t *testing.T, capName string) {
	t.Helper()
	old := loadRowConnector
	t.Cleanup(func() { loadRowConnector = old })
	loadRowConnector = func(ctx context.Context, row backendrows.Row, o backendrows.Options) (*connectorhost.Connector, error) {
		conn, err := old(ctx, row, o)
		if conn != nil {
			conn.Capabilities = slices.DeleteFunc(slices.Clone(conn.Capabilities), func(c string) bool { return c == capName })
		}
		return conn, err
	}
}

// A shadow github row never routes: gh stays authoritative for every host
// read, and the shadow compares gh against the Connector. One subtest per read
// behind routedRow, so a site that consults the row directly and skips the
// Shadow check fails here.
func TestRoutedReads_ShadowRowLeavesGhAnswering(t *testing.T) {
	for _, tc := range []struct {
		name string
		read func(c *execClient)
	}{
		{"CountReady", func(c *execClient) { _, _ = c.CountReady(false) }},
		{"ListIssues", func(c *execClient) { _, _ = c.ListIssues(forge.Dispatchable) }},
		{"PriorClaimState", func(c *execClient) { _, _, _ = c.PriorClaimState("1") }},
		{"Probe", func(c *execClient) { _, _ = c.Probe() }},
		{"ListLabels", func(c *execClient) { _, _ = c.ListLabels() }},
		{"BranchProtected", func(c *execClient) { _, _ = c.BranchProtected("main") }},
		{"CanAutoMerge", func(c *execClient) { _, _ = c.CanAutoMerge() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, c, calls := harnessWithRow(t, useGithubRowShadow)

			tc.read(c)

			if calls() == 0 {
				t.Errorf("%s under a shadow row never reached gh", tc.name)
			}
		})
	}
}

// Under a github row the host-side reads doctor, the daemon and recover make
// are answered by the Connector over the API, never by gh.
func TestExecClient_ConfiguredRowReadsGoThroughTheConnector(t *testing.T) {
	h, c, calls := routedHarness(t)
	h.SeedProtection("main", 200, 0)
	h.SeedProtection("dev", 404, 0)
	h.SeedProtection("rel", 404, 2)
	h.SeedProtection("locked", 403, 0)
	for _, tc := range []struct {
		branch string
		want   bool
	}{{"main", true}, {"dev", false}, {"rel", true}} {
		if got, err := c.BranchProtected(tc.branch); err != nil || got != tc.want {
			t.Errorf("BranchProtected(%q) = %v, %v; want %v", tc.branch, got, err, tc.want)
		}
	}
	if _, err := c.BranchProtected("locked"); err == nil || errors.Is(err, backendkit.ErrUnsupported) {
		t.Errorf("BranchProtected of an unreadable branch = %v, want a failure that is not unsupported", err)
	}

	for _, allowed := range []bool{true, false} {
		h.backend.autoMerge = allowed
		if got, err := c.CanAutoMerge(); err != nil || got != allowed {
			t.Errorf("CanAutoMerge = %v, %v; want %v", got, err, allowed)
		}
	}

	ready, active := []string{testLabels.Dispatchable}, []string{testLabels.InProgress}
	for i, labels := range [][]string{ready, ready, active, ready, nil} {
		num := string(rune('1' + i))
		h.SeedIssue(forge.Issue{Number: num, Title: num, Labels: labels})
	}
	h.mutate("2", func(i *restIssue) { i.pull = true })

	for _, fresh := range []bool{false, true} {
		if n, err := c.CountReady(fresh); err != nil || n != 2 {
			t.Errorf("CountReady(%v) = %d, %v; want 2", fresh, n, err)
		}
	}

	h.mutate("5", func(i *restIssue) { i.unlabeled = []string{testLabels.Failed, testLabels.Complete} })
	if st, found, err := c.PriorClaimState("5"); err != nil || !found || st != forge.Complete {
		t.Errorf("PriorClaimState = %v, %v, %v; want Complete", st, found, err)
	}

	if got, err := c.ListIssues(forge.Dispatchable); err != nil || len(got) != 2 || got[0].Number != "1" || got[1].Number != "4" {
		t.Errorf("ListIssues(Dispatchable) = %v, %v; want issues 1 and 4", got, err)
	}

	h.backend.labels = []string{"a", "b"}
	if got, err := c.ListLabels(); err != nil || !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("ListLabels = %v, %v; want [a b]", got, err)
	}

	if n := calls(); n != 0 {
		t.Errorf("the routed reads ran gh %d times", n)
	}
}

// A Connector that does not declare the capability a read needs makes that
// read unsupported, and the host does not fall back to gh.
func TestExecClient_ConfiguredRowWithoutTheCapabilityIsUnsupported(t *testing.T) {
	for _, tc := range []struct {
		capName string
		read    func(*execClient) error
	}{
		{"forge.probe", func(c *execClient) error { _, err := c.Probe(); return err }},
		{"forge.branchProtection", func(c *execClient) error {
			_, err := c.BranchProtected("main")
			return err
		}},
		{"forge.autoMerge", func(c *execClient) error { _, err := c.CanAutoMerge(); return err }},
		{connectortracker.CapCount, func(c *execClient) error { _, err := c.CountReady(false); return err }},
		{connectortracker.CapRemovedLabels, func(c *execClient) error {
			_, _, err := c.PriorClaimState("7")
			return err
		}},
		{connectortracker.CapLabels, func(c *execClient) error { _, err := c.ListLabels(); return err }},
	} {
		t.Run(tc.capName, func(t *testing.T) {
			_, c, calls := routedHarness(t)
			withoutCapability(t, tc.capName)

			err := tc.read(c)

			if !errors.Is(err, backendkit.ErrUnsupported) {
				t.Errorf("a Connector without %s: err = %v, want backendkit.ErrUnsupported", tc.capName, err)
			}
			if n := calls(); n != 0 {
				t.Errorf("the read ran gh %d times", n)
			}
		})
	}
}

// Every routed read, from every client, shares one loaded Connector per
// repository, and a failed load is retried rather than remembered.
func TestExecClient_ConfiguredRowLoadsTheConnectorOnce(t *testing.T) {
	_, _, _ = routedHarness(t)
	var loads, failFirst atomic.Int32
	failFirst.Store(1)
	old := loadRowConnector
	t.Cleanup(func() { loadRowConnector = old })
	loadRowConnector = func(ctx context.Context, row backendrows.Row, o backendrows.Options) (*connectorhost.Connector, error) {
		loads.Add(1)
		if failFirst.Add(-1) >= 0 {
			return nil, errors.New("load failed")
		}
		return old(ctx, row, o)
	}

	if _, err := NewExecClient(connectorRepo, testLabels).ListLabels(); err == nil || errors.Is(err, backendkit.ErrUnsupported) {
		t.Fatalf("ListLabels with a failing load = %v, want the load error", err)
	}
	for range 2 {
		c := NewExecClient(connectorRepo, testLabels)
		if _, err := c.ListLabels(); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Probe(); err != nil {
			t.Fatal(err)
		}
		if _, err := c.BranchProtected("main"); err != nil {
			t.Fatal(err)
		}
	}
	if n := loads.Load(); n != 2 {
		t.Errorf("Connector loaded %d times, want 2 (the failed attempt, then one shared load)", n)
	}
}

func TestUnsupportedWithout_MapsOnlyAMissingCapability(t *testing.T) {
	boom, missing := errors.New("boom"), errors.New("missing")
	if err := unsupportedWithout(boom, "read", missing); !errors.Is(err, boom) || errors.Is(err, backendkit.ErrUnsupported) {
		t.Errorf("a failed read = %v, want it passed through, not unsupported", err)
	}
	if err := unsupportedWithout(fmt.Errorf("wrapped: %w", missing), "read", boom, missing); !errors.Is(err, backendkit.ErrUnsupported) {
		t.Errorf("a Connector lacking the capability = %v, want backendkit.ErrUnsupported", err)
	}
	if err := unsupportedWithout(nil, "read", missing); err != nil {
		t.Errorf("nil = %v, want nil", err)
	}
}
