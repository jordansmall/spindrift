package github

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
	"spindrift.dev/launcher/internal/credsource/credsourcetest"
	"spindrift.dev/launcher/internal/forge"
)

// testLabels mirrors the lifecycle labels in lib/env-schema.nix (issue #460),
// so no test has to restate the label strings.
var testLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
	Ambiguous:    "agent-ambiguous-spec",
}

func prNum(ref string) string {
	parts := strings.Split(ref, "/")
	return parts[len(parts)-1]
}

// prependFakeGH puts a counting-wrapper gh script on PATH and returns its dir.
// Each invocation records its argv to call-NN.txt (zero-indexed) in that dir.
func prependFakeGH(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
n=$(ls "%s"/call-*.txt 2>/dev/null | wc -l)
printf '%%s\n' "$@" > "%s/call-$(printf '%%02d' $n).txt"
%s`, dir, dir, body)
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", old) })
	os.Setenv("PATH", dir+":"+old)
	return dir
}

// newClient is NewExecClient over a runtime of its own, closed with the test.
func newClient(t *testing.T, repo string, labels forge.DispatchLabels, opts ...ExecOption) *execClient {
	t.Helper()
	return NewExecClient(repo, labels, append([]ExecOption{WithConnectors(testRuntime(t))}, opts...)...)
}

// testRuntime is a Connector runtime over the rows applyGithubRow applied and the installed
// sandbox, closed with the test.
func testRuntime(t *testing.T) *connectorruntime.Runtime {
	t.Helper()
	rt := connectorruntime.New(appliedRows, credsourcetest.EnvRows(appliedRows.All()...), testSandbox())
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

// hookedSandbox is the runtime's sandbox: the in-process engine behind the load
// hook a test installs with hookLoads.
type hookedSandbox struct{ connectorsandbox.Loader }

var loadHook func(load func() (*connectorhost.Connector, error)) (*connectorhost.Connector, error)

func (s hookedSandbox) LoadNetwork(ctx context.Context, wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error) {
	load := func() (*connectorhost.Connector, error) { return s.Loader.LoadNetwork(ctx, wasm, n) }
	if loadHook != nil {
		return loadHook(load)
	}
	return load()
}

// hookLoads runs hook around every Connector load the test makes, after any
// hook already installed; hook gets the load it wraps.
func hookLoads(t *testing.T, hook func(load func() (*connectorhost.Connector, error)) (*connectorhost.Connector, error)) {
	t.Helper()
	prev := loadHook
	t.Cleanup(func() { loadHook = prev })
	loadHook = func(load func() (*connectorhost.Connector, error)) (*connectorhost.Connector, error) {
		if prev == nil {
			return hook(load)
		}
		return hook(func() (*connectorhost.Connector, error) { return prev(load) })
	}
}

// testSandbox is the in-process engine behind the hook hookLoads installs, the
// sandbox every test runtime loads into: a test binary has no runner to start
// one on.
func testSandbox() connectorsandbox.Loader { return hookedSandbox{connectorsandboxtest.InProcess()} }
