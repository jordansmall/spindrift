package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestWalkGateRegistry_CallsEveryGateInOrder proves walkGateRegistry visits
// every gate in a passing registry, in registry order.
func TestWalkGateRegistry_CallsEveryGateInOrder(t *testing.T) {
	var calls []string
	registry := []launchGate{
		{Name: "first", Check: func(config, io.Writer) error { calls = append(calls, "first"); return nil }},
		{Name: "second", Check: func(config, io.Writer) error { calls = append(calls, "second"); return nil }},
		{Name: "third", Check: func(config, io.Writer) error { calls = append(calls, "third"); return nil }},
	}

	var buf bytes.Buffer
	if err := walkGateRegistry(registry, config{}, &buf, &buf, false); err != nil {
		t.Fatalf("walkGateRegistry() unexpected error: %v", err)
	}

	want := []string{"first", "second", "third"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("call order = %v, want %v", calls, want)
	}
}

// TestWalkGateRegistry_StopsAtFirstFailure proves walkGateRegistry returns
// the failing gate's error immediately and never invokes any gate after it,
// when collectAll is false.
func TestWalkGateRegistry_StopsAtFirstFailure(t *testing.T) {
	var calls []string
	wantErr := errors.New("second gate failed")
	registry := []launchGate{
		{Name: "first", Check: func(config, io.Writer) error { calls = append(calls, "first"); return nil }},
		{Name: "second", Check: func(config, io.Writer) error { calls = append(calls, "second"); return wantErr }},
		{Name: "third", Check: func(config, io.Writer) error { calls = append(calls, "third"); return nil }},
	}

	var buf bytes.Buffer
	err := walkGateRegistry(registry, config{}, &buf, &buf, false)
	if !errors.Is(err, wantErr) {
		t.Fatalf("walkGateRegistry() error = %v, want %v", err, wantErr)
	}

	want := []string{"first", "second"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("call order = %v, want %v (third must never be called)", calls, want)
	}
}

// TestWalkGateRegistry_SkipsInapplicableGateEntirely proves a gate whose
// Applicable(c) is false never has its Check invoked and never produces a
// report line -- a gate self-noop'd out by backend selection (e.g. the
// forgejo token gate on a github-only config) must not print a false "ok"
// for a check that never ran against anything real (issue #2942 bug 2).
func TestWalkGateRegistry_SkipsInapplicableGateEntirely(t *testing.T) {
	skippedCalled := false
	registry := []launchGate{
		{Name: "first", Check: func(config, io.Writer) error { return nil }},
		{
			Name:       "skipped",
			Applicable: func(config) bool { return false },
			Check:      func(config, io.Writer) error { skippedCalled = true; return nil },
		},
		{Name: "third", Check: func(config, io.Writer) error { return nil }},
	}

	var buf bytes.Buffer
	if err := walkGateRegistry(registry, config{}, &buf, &buf, false); err != nil {
		t.Fatalf("walkGateRegistry() unexpected error: %v", err)
	}

	if skippedCalled {
		t.Error("walkGateRegistry() invoked an inapplicable gate's Check, want it skipped entirely")
	}
	if strings.Contains(buf.String(), "skipped") {
		t.Errorf("walkGateRegistry() output = %q, want no mention of the inapplicable gate", buf.String())
	}
}

// TestWalkGateRegistry_FailingGateWritesMissingReportLine proves a failing
// gate's Check produces a "MISSING: <name>: <err>" row on reportW, matching
// the doctor/registry.go convention (`fmt.Fprintf(w, "MISSING: %s: %s\n",
// r.Check.Name, r.Err)`) and docs/reference.md's stdout convention -- a
// failing gate must never leave the report silent (code-review finding on
// issue #2942), whether or not the walk then stops (collectAll=false here).
func TestWalkGateRegistry_FailingGateWritesMissingReportLine(t *testing.T) {
	wantErr := errors.New("first gate failed")
	registry := []launchGate{
		{Name: "first", Check: func(config, io.Writer) error { return wantErr }},
	}

	var buf bytes.Buffer
	err := walkGateRegistry(registry, config{}, &buf, &buf, false)
	if !errors.Is(err, wantErr) {
		t.Fatalf("walkGateRegistry() error = %v, want %v", err, wantErr)
	}

	want := "MISSING: first: first gate failed\n"
	if !strings.Contains(buf.String(), want) {
		t.Errorf("walkGateRegistry() output = %q, want it to contain %q", buf.String(), want)
	}
}

// TestWalkGateRegistry_CollectAllRunsEveryNonNetworkGateBeforeStopping
// proves collectAll=true keeps walking past a failing non-network gate --
// `spindrift doctor` must enumerate every simultaneously-broken non-network
// knob, not just the first (issue #2942 bug 3 / docs/reference.md's exit-2
// contract).
func TestWalkGateRegistry_CollectAllRunsEveryNonNetworkGateBeforeStopping(t *testing.T) {
	firstErr := errors.New("first gate failed")
	secondErr := errors.New("second gate failed")
	var firstCalled, secondCalled, thirdCalled bool
	registry := []launchGate{
		{Name: "first", Check: func(config, io.Writer) error { firstCalled = true; return firstErr }},
		{Name: "second", Check: func(config, io.Writer) error { secondCalled = true; return secondErr }},
		{Name: "third", Network: true, Check: func(config, io.Writer) error { thirdCalled = true; return nil }},
	}

	var buf bytes.Buffer
	err := walkGateRegistry(registry, config{}, &buf, &buf, true)

	if !firstCalled || !secondCalled {
		t.Fatalf("firstCalled=%v secondCalled=%v, want both non-network gates invoked despite the first failing", firstCalled, secondCalled)
	}
	if !thirdCalled {
		t.Error("thirdCalled = false, want the trailing network gate still reached after two non-network failures")
	}
	if !errors.Is(err, firstErr) {
		t.Errorf("walkGateRegistry() error = %v, want it to wrap firstErr", err)
	}
	if !errors.Is(err, secondErr) {
		t.Errorf("walkGateRegistry() error = %v, want it to wrap secondErr", err)
	}

	out := buf.String()
	if !strings.Contains(out, "MISSING: first: first gate failed") {
		t.Errorf("walkGateRegistry() output = %q, want a MISSING row for the first failing gate even though the walk continued past it", out)
	}
	if !strings.Contains(out, "MISSING: second: second gate failed") {
		t.Errorf("walkGateRegistry() output = %q, want a MISSING row for the second failing gate even though the walk continued past it", out)
	}
}

// TestWalkGateRegistry_CollectAllStopsAtFailingNetworkGate proves collectAll
// resumes fail-fast behavior once it reaches a failing Network gate: a live
// network probe after an already-failed one is moot, so the gate after it
// must never be invoked (issue #2942 bug 3, docs/reference.md's exit-3
// connectivity probes staying fail-fast).
func TestWalkGateRegistry_CollectAllStopsAtFailingNetworkGate(t *testing.T) {
	networkErr := errors.New("network gate failed")
	var afterCalled bool
	registry := []launchGate{
		{Name: "first", Check: func(config, io.Writer) error { return nil }},
		{Name: "network", Network: true, Check: func(config, io.Writer) error { return networkErr }},
		{Name: "after", Check: func(config, io.Writer) error { afterCalled = true; return nil }},
	}

	var buf bytes.Buffer
	err := walkGateRegistry(registry, config{}, &buf, &buf, true)

	if afterCalled {
		t.Error("walkGateRegistry() invoked the gate after a failing network gate, want fail-fast resumed")
	}
	if !errors.Is(err, networkErr) {
		t.Errorf("walkGateRegistry() error = %v, want it to wrap networkErr", err)
	}
}

// TestWalkGateRegistry_CollectAllFalseSingleErrorTextUnchanged proves the
// errors.Join-of-one wrapping in the fail-fast (collectAll=false) path does
// not alter the error text or errors.Is/As behavior enforcement callers
// already depend on -- errors.Join(err) behaves identically to returning err
// bare for a single-element slice.
func TestWalkGateRegistry_CollectAllFalseSingleErrorTextUnchanged(t *testing.T) {
	sentinel := errors.New("sentinel")
	wrapped := fmt.Errorf("boom: %w", sentinel)
	registry := []launchGate{
		{Name: "only", Check: func(config, io.Writer) error { return wrapped }},
	}

	var buf bytes.Buffer
	got := walkGateRegistry(registry, config{}, &buf, &buf, false)

	if got.Error() != "boom: sentinel" {
		t.Fatalf("walkGateRegistry() error text = %q, want %q", got.Error(), "boom: sentinel")
	}
	if !errors.Is(got, sentinel) {
		t.Error("errors.Is(got, sentinel) = false, want true through errors.Join's Unwrap() []error")
	}
}

// TestGateRegistry_EnforceOrderEqualsReportOrder proves AC4 through the two
// REAL production entry points -- newGatedContext (enforcement) and runDoctor
// (reporting) -- rather than two direct walkGateRegistry(gateRegistry, ...)
// calls (the old version of this test), which derived both "orders" from the
// same raw gateRegistry slice and so could never observe enforcement and
// reporting reading gateRegistry through two different constructions.
//
// gateRegistry is swapped for a copy of itself plus one always-passing
// trailing non-Network probe gate (save/restore, the same technique
// backend_extensibility_test.go's backendRows swap and
// TestDoctorGateRegistryReport_FailingGate_ErrorPropagatesAndPriorGatesReport's
// gateRegistry swap use), so the guarantee is proven for a registry shape a
// future edit could produce, not just today's four entries.
//
// ghTokenIntrospector is also swapped to a stub that passes without a live
// call (the seam readonly_token_gate.go itself documents tests using), and
// BOX_FORGE_AND_ISSUE_ACCESS/CODE_FORGE/ISSUE_TRACKER are set so the
// read-only-token-github gate is Applicable and passes while
// read-only-token-forgejo stays inapplicable. This is deliberate: with both
// token gates inapplicable (invisible to either order, since an inapplicable
// gate produces no report line under raw declaration order or under the
// split), a probe appended after them can never land in a different visible
// position under the two constructions, so a test built that way cannot
// RED-fail against the pre-fix doctor.go tail. Making read-only-token-github
// visible -- declared after network-mode-runtime, before the probe -- is what
// makes the two constructions actually diverge: pre-fix, doctor prints
// capability, network-mode-runtime, read-only-token-github, probe (raw
// declaration order); post-fix (and in newGatedContext's own construction),
// it's capability, network-mode-runtime, probe, read-only-token-github (the
// non-Network bucket exhausted before the Network bucket starts).
//
// newGatedContext itself is still called and required to succeed, proving
// the real enforcement entry point walks this same registry+config
// combination cleanly end to end. Its own report lines aren't inspected,
// though: newGatedContext hands walkGateRegistry io.Discard as reportW by
// design (AC5, gatedcontext.go), so it never writes an "ok: <name>" line for
// any gate, real or probe, regardless of order -- there is nothing in its
// writer to compare against doctor's report text. The "enforce order" this
// test checks doctor's report against is instead built by applying
// walkGateRegistry's own Applicable filter to splitGateRegistryByNetwork(gateRegistry)
// -- the same production split function newGatedContext calls -- which is
// exactly the order newGatedContext's construction implies its writer would
// show if AC5 didn't suppress it.
func TestGateRegistry_EnforceOrderEqualsReportOrder(t *testing.T) {
	originalRegistry := gateRegistry
	gateRegistry = append(append([]launchGate{}, originalRegistry...), launchGate{
		Name:  "zz-probe",
		Check: func(config, io.Writer) error { return nil },
	})
	defer func() { gateRegistry = originalRegistry }()

	originalIntrospector := ghTokenIntrospector
	ghTokenIntrospector = func(string, string) (tokenIntrospectionResult, error) {
		return tokenIntrospectionResult{Introspectable: true, WriteCapable: false}, nil
	}
	defer func() { ghTokenIntrospector = originalIntrospector }()

	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "ghp_test")
	t.Setenv("GIT_USER_NAME", "bot")
	t.Setenv("GIT_USER_EMAIL", "bot@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
	t.Setenv("CODE_FORGE", "github")
	t.Setenv("ISSUE_TRACKER", "github")
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("MERGE_MODE", "manual")
	t.Setenv("RUNTIME", "echo")
	t.Setenv("BOX_FORGE_AND_ISSUE_ACCESS", "read-only")
	t.Setenv("BOX_GH_TOKEN", "box-gh-token-distinct-from-launcher-token")

	var enforceBuf bytes.Buffer
	if _, err := newGatedContext(&enforceBuf, dispatchKindWork, false); err != nil {
		t.Fatalf("newGatedContext() unexpected error: %v", err)
	}

	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"

	var reportBuf bytes.Buffer
	if err := runDoctor(f, f, c, &reportBuf, strings.NewReader(""), false); err != nil {
		t.Fatalf("runDoctor() unexpected error: %v", err)
	}
	// doctor.Run's own launcherChecks/label/runtime probes also print "ok:
	// <text>" lines (repo-slug, git-user-name, ...) ahead of the gateRegistry
	// section -- gateNamesFromOkLines has no way to tell those apart from a
	// gate's own "ok: <name>" line, so filter down to gateRegistry's own
	// names before comparing order.
	reportOrder := gateNamesInRegistry(gateNamesFromOkLines(reportBuf.String()), gateRegistry)

	nonNetwork, network := splitGateRegistryByNetwork(gateRegistry)
	want := append(applicableGateNames(nonNetwork, c), applicableGateNames(network, c)...)

	if !reflect.DeepEqual(reportOrder, want) {
		t.Fatalf("runDoctor() report order = %v, want %v (splitGateRegistryByNetwork(gateRegistry) order)", reportOrder, want)
	}
	if want[len(want)-2] != "zz-probe" {
		t.Fatalf("want = %v, probe gate expected second-to-last (last of the non-Network bucket, before the Network bucket's read-only-token-github)", want)
	}
}

// applicableGateNames names each gate in gates whose Applicable(c) is true
// (or nil, meaning always applicable), in order -- the same filter
// walkGateRegistry itself applies before ever calling Check, reproduced here
// to compute the report-line order a registry construction implies without
// needing a real writer.
func applicableGateNames(gates []launchGate, c config) []string {
	var names []string
	for _, g := range gates {
		if g.Applicable != nil && !g.Applicable(c) {
			continue
		}
		names = append(names, g.Name)
	}
	return names
}

// gateNamesInRegistry filters names down to the ones that are actually a
// gate's Name in registry, preserving order -- used to drop an unrelated "ok:
// <text>" line (e.g. doctor's own launcherChecks/label probes) that happens
// to share gateNamesFromOkLines' "ok: " prefix convention.
func gateNamesInRegistry(names []string, registry []launchGate) []string {
	known := make(map[string]bool, len(registry))
	for _, g := range registry {
		known[g.Name] = true
	}
	var filtered []string
	for _, n := range names {
		if known[n] {
			filtered = append(filtered, n)
		}
	}
	return filtered
}

// gateNamesFromOkLines extracts the gate name from each "ok: <name>" line in
// out, in order, ignoring any other line -- e.g. a token gate's own WARNING
// print, which lands in the same buffer when checkW and reportW are the same
// writer (as doctor.go's real call site passes them), a shape this helper's
// caller (TestGateRegistry_EnforceOrderEqualsReportOrder) exercises under
// BOX_FORGE_AND_ISSUE_ACCESS=read-only so read-only-token-github (only)
// stays Applicable.
func gateNamesFromOkLines(out string) []string {
	var names []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if !strings.HasPrefix(line, "ok: ") {
			continue
		}
		names = append(names, strings.TrimPrefix(line, "ok: "))
	}
	return names
}

// TestGateRegistry_TokenGatesInapplicableUnderReadWrite proves the real
// gateRegistry's two token gate entries (read-only-token-github,
// read-only-token-forgejo) are entirely skipped under
// BOX_FORGE_AND_ISSUE_ACCESS=read-write, even when their backend matches --
// codeForge/issueTracker=github here. Before this fix, Applicable checked
// only backend match, so walkGateRegistry called Check (which self-noops
// under read-write and returns nil) and printed a false "ok:
// read-only-token-github" for a check that, per the Applicable field's own
// doc comment, never ran against anything real (code-review finding on issue
// #2942). Applicable must also require read-only, so the gate is skipped
// outright: no Check call, no report line at all.
func TestGateRegistry_TokenGatesInapplicableUnderReadWrite(t *testing.T) {
	c := minimalValidConfig() // boxForgeAndIssueAccess: read-write, codeForge/issueTracker: github

	var buf bytes.Buffer
	if err := walkGateRegistry(gateRegistry, c, &buf, &buf, true); err != nil {
		t.Fatalf("walkGateRegistry() unexpected error: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "read-only-token-github") {
		t.Errorf("walkGateRegistry() output = %q, want no mention of read-only-token-github under read-write", out)
	}
	if strings.Contains(out, "read-only-token-forgejo") {
		t.Errorf("walkGateRegistry() output = %q, want no mention of read-only-token-forgejo under read-write", out)
	}
}

// TestSplitGateRegistryByNetwork_PartitionsByFieldNotPosition proves
// splitGateRegistryByNetwork derives its split purely from each gate's own
// Network field, preserving order within each half, regardless of where the
// network gates fall positionally. The fixture registry below interleaves
// non-network and network gates (network, non-network, non-network,
// network, non-network) -- a shape a hardcoded index like gateRegistry[:2]/
// gateRegistry[2:] could never partition correctly, since no single split
// point separates the two groups here. This is the regression the previous
// index-based newGatedContext could never have been caught by: inserting or
// reordering gateRegistry's real gates couldn't be exercised meaningfully
// against fixed indices, since the indices themselves encoded the very
// ordering assumption under test.
func TestSplitGateRegistryByNetwork_PartitionsByFieldNotPosition(t *testing.T) {
	registry := []launchGate{
		{Name: "net-a", Network: true},
		{Name: "local-a"},
		{Name: "local-b"},
		{Name: "net-b", Network: true},
		{Name: "local-c"},
	}

	nonNetwork, network := splitGateRegistryByNetwork(registry)

	wantNonNetwork := []string{"local-a", "local-b", "local-c"}
	wantNetwork := []string{"net-a", "net-b"}
	if got := gateNames(nonNetwork); !reflect.DeepEqual(got, wantNonNetwork) {
		t.Errorf("splitGateRegistryByNetwork() nonNetwork = %v, want %v", got, wantNonNetwork)
	}
	if got := gateNames(network); !reflect.DeepEqual(got, wantNetwork) {
		t.Errorf("splitGateRegistryByNetwork() network = %v, want %v", got, wantNetwork)
	}
}

// gateNames extracts each gate's Name in order, for assertions against
// splitGateRegistryByNetwork's two output slices.
func gateNames(gates []launchGate) []string {
	names := make([]string, len(gates))
	for i, g := range gates {
		names[i] = g.Name
	}
	return names
}
