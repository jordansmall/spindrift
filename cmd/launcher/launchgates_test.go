package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
)

func TestWalkGateRegistry_CallsEveryGateInOrder(t *testing.T) {
	var calls []string
	registry := []launchGate{
		{Name: "first", Check: func(config, io.Writer) error { calls = append(calls, "first"); return nil }},
		{Name: "second", Check: func(config, io.Writer) error { calls = append(calls, "second"); return nil }},
		{Name: "third", Check: func(config, io.Writer) error { calls = append(calls, "third"); return nil }},
	}

	var buf bytes.Buffer
	if err := walkGateRegistry(registry, config{}, &buf, doctor.NewReporter(&buf, true), walkEnforce); err != nil {
		t.Fatalf("walkGateRegistry() unexpected error: %v", err)
	}

	want := []string{"first", "second", "third"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("call order = %v, want %v", calls, want)
	}
}

func TestWalkGateRegistry_StopsAtFirstFailure(t *testing.T) {
	var calls []string
	wantErr := errors.New("second gate failed")
	registry := []launchGate{
		{Name: "first", Check: func(config, io.Writer) error { calls = append(calls, "first"); return nil }},
		{Name: "second", Check: func(config, io.Writer) error { calls = append(calls, "second"); return wantErr }},
		{Name: "third", Check: func(config, io.Writer) error { calls = append(calls, "third"); return nil }},
	}

	var buf bytes.Buffer
	err := walkGateRegistry(registry, config{}, &buf, doctor.NewReporter(&buf, true), walkEnforce)
	if !errors.Is(err, wantErr) {
		t.Fatalf("walkGateRegistry() error = %v, want %v", err, wantErr)
	}

	want := []string{"first", "second"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("call order = %v, want %v (third must never be called)", calls, want)
	}
}

// A gate that backend selection noops out, such as the github Connector
// write gate on a non-github config, must not print a false "ok" for a check
// that never ran against anything real (issue #2942 bug 2).
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
	if err := walkGateRegistry(registry, config{}, &buf, doctor.NewReporter(&buf, true), walkEnforce); err != nil {
		t.Fatalf("walkGateRegistry() unexpected error: %v", err)
	}

	if skippedCalled {
		t.Error("walkGateRegistry() invoked an inapplicable gate's Check, want it skipped entirely")
	}
	if strings.Contains(buf.String(), "skipped") {
		t.Errorf("walkGateRegistry() output = %q, want no mention of the inapplicable gate", buf.String())
	}
}

// A failing gate must never leave the report silent, whether or not the walk
// then stops (issue #2942). The row format follows doctor/registry.go and
// docs/reference.md's stdout convention.
func TestWalkGateRegistry_FailingGateWritesMissingReportLine(t *testing.T) {
	wantErr := errors.New("first gate failed")
	registry := []launchGate{
		{Name: "first", Check: func(config, io.Writer) error { return wantErr }},
	}

	var buf bytes.Buffer
	err := walkGateRegistry(registry, config{}, &buf, doctor.NewReporter(&buf, true), walkEnforce)
	if !errors.Is(err, wantErr) {
		t.Fatalf("walkGateRegistry() error = %v, want %v", err, wantErr)
	}

	want := "MISSING: first: first gate failed\n"
	if !strings.Contains(buf.String(), want) {
		t.Errorf("walkGateRegistry() output = %q, want it to contain %q", buf.String(), want)
	}
}

// `spindrift doctor` must enumerate every simultaneously-broken non-network
// knob, not just the first (issue #2942 bug 3, docs/reference.md's exit-2
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
	err := walkGateRegistry(registry, config{}, &buf, doctor.NewReporter(&buf, true), walkReport)

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

// A live network probe after an already-failed one is moot, so collectAll
// resumes fail-fast at a failing Network gate (issue #2942 bug 3,
// docs/reference.md's exit-3 connectivity probes).
func TestWalkGateRegistry_CollectAllStopsAtFailingNetworkGate(t *testing.T) {
	networkErr := errors.New("network gate failed")
	var afterCalled bool
	registry := []launchGate{
		{Name: "first", Check: func(config, io.Writer) error { return nil }},
		{Name: "network", Network: true, Check: func(config, io.Writer) error { return networkErr }},
		{Name: "after", Check: func(config, io.Writer) error { afterCalled = true; return nil }},
	}

	var buf bytes.Buffer
	err := walkGateRegistry(registry, config{}, &buf, doctor.NewReporter(&buf, true), walkReport)

	if afterCalled {
		t.Error("walkGateRegistry() invoked the gate after a failing network gate, want fail-fast resumed")
	}
	if !errors.Is(err, networkErr) {
		t.Errorf("walkGateRegistry() error = %v, want it to wrap networkErr", err)
	}
}

// The fail-fast path wraps a lone error in errors.Join, which must not change
// the error text or the errors.Is behavior enforcement callers depend on.
func TestWalkGateRegistry_CollectAllFalseSingleErrorTextUnchanged(t *testing.T) {
	sentinel := errors.New("sentinel")
	wrapped := fmt.Errorf("boom: %w", sentinel)
	registry := []launchGate{
		{Name: "only", Check: func(config, io.Writer) error { return wrapped }},
	}

	var buf bytes.Buffer
	got := walkGateRegistry(registry, config{}, &buf, doctor.NewReporter(&buf, true), walkEnforce)

	if got.Error() != "boom: sentinel" {
		t.Fatalf("walkGateRegistry() error text = %q, want %q", got.Error(), "boom: sentinel")
	}
	if !errors.Is(got, sentinel) {
		t.Error("errors.Is(got, sentinel) = false, want true through errors.Join's Unwrap() []error")
	}
}

// AC4 goes through the two real entry points, newGatedContext and runDoctor,
// so enforcement and reporting each build their own order instead of both
// reading the raw gateRegistry slice. The appended Network probe precedes the
// non-Network one, so only a walk that partitions by Network puts them in
// the other order.
func TestGateRegistry_EnforceOrderEqualsReportOrder(t *testing.T) {
	originalRegistry := gateRegistry
	gateRegistry = append(append([]launchGate{}, originalRegistry...),
		launchGate{Name: "zz-net", Network: true, Check: func(config, io.Writer) error { return nil }},
		launchGate{Name: "zz-probe", Check: func(config, io.Writer) error { return nil }},
	)
	defer func() { gateRegistry = originalRegistry }()

	setKnob(t, "REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "ghp_test")
	setKnob(t, "GIT_USER_NAME", "bot")
	setKnob(t, "GIT_USER_EMAIL", "bot@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
	setKnob(t, "CODE_FORGE", "github")
	setKnob(t, "ISSUE_TRACKER", "github")
	setKnob(t, "BASE_BRANCH", "main")
	setKnob(t, "MERGE_MODE", "manual")
	setArtifact(t, "RUNTIME", "echo")
	setKnob(t, "BOX_FORGE_AND_ISSUE_ACCESS", "read-only")

	var enforceBuf bytes.Buffer
	if _, err := newGatedContext(&enforceBuf, dispatchkind.Work, false); err != nil {
		t.Fatalf("newGatedContext() unexpected error: %v", err)
	}

	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	c := minimalValidConfig()
	c.BoxForgeAndIssueAccess = "read-only"
	c.Label, c.InProgressLabel, c.FailedLabel, c.CompleteLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"

	var reportBuf bytes.Buffer
	_, report := doctorCheckSets(c)
	if err := runDoctor(f, f, c, doctor.NewReporter(&reportBuf, true), &reportBuf, strings.NewReader(""), false, report); err != nil {
		t.Fatalf("runDoctor() unexpected error: %v", err)
	}
	// doctor.Run's extra rows (doctorCheckSets' report half), label and runtime
	// probes print "ok: <text>" lines ahead of the gateRegistry section, which
	// gateNamesFromOkLines cannot tell apart from a gate's own line.
	reportOrder := gateNamesInRegistry(gateNamesFromOkLines(reportBuf.String()), gateRegistry)

	// newGatedContext hands walkGateRegistry a Reporter discarding to io.Discard
	// (AC5), so its writer holds no "ok:" lines to compare. Recompute the order
	// it implies.
	nonNetwork, network := splitGateRegistryByNetwork(gateRegistry)
	want := append(applicableGateNames(nonNetwork, c), applicableGateNames(network, c)...)

	if !reflect.DeepEqual(reportOrder, want) {
		t.Fatalf("runDoctor() report order = %v, want %v (splitGateRegistryByNetwork(gateRegistry) order)", reportOrder, want)
	}
	if want[len(want)-2] != "zz-probe" || want[len(want)-1] != "zz-net" {
		t.Fatalf("want = %v, expected zz-probe (last non-Network) then zz-net (Network bucket)", want)
	}
}

// applicableGateNames reproduces the filter walkGateRegistry applies before it
// calls any Check, so a registry construction's report order can be computed
// without a real writer.
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

// gateNamesInRegistry drops names that belong to no gate, so an unrelated "ok:
// <text>" line sharing the same prefix cannot shift the order.
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

// gateNamesFromOkLines ignores every other line, such as a gate's WARNING
// print, which lands in the same buffer when checkW and reportW are the same
// writer, as doctor.go's real call site passes them.
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

// The fixture interleaves network and non-network gates so that no single split
// point separates the two groups, a shape a hardcoded index like
// gateRegistry[:2]/gateRegistry[2:] could never partition correctly.
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

func gateNames(gates []launchGate) []string {
	names := make([]string, len(gates))
	for i, g := range gates {
		names[i] = g.Name
	}
	return names
}

// A read-only Box holds no forge token, so no gate scope-checks one: the
// Connector write gate is the registry's only network gate.
func TestGateRegistry_OnlyNetworkGateIsConnectorWrite(t *testing.T) {
	_, network := splitGateRegistryByNetwork(gateRegistry)
	// The literal pins the user-visible gate id, not the constant.
	want := []string{"connector-write-github"}
	if got := gateNames(network); !reflect.DeepEqual(got, want) {
		t.Errorf("network gates = %v, want %v", got, want)
	}
}
