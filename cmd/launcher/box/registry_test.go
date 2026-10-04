package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/bindregistry"
	"spindrift.dev/launcher/internal/ecosystem"
)

const (
	bindWarning   = "==> WARNING: driver-exec bind-registry failed (exit 1) — skipping registry proxy bindings"
	applyWarning  = "==> WARNING: driver-exec bind-registry (in-tree apply) failed (exit 1) — skipping in-tree registry binding"
	revertWarning = "==> WARNING: driver-exec bind-registry (in-tree revert) failed (exit 1)"
)

// regFake stands in for the registry proxy steps and records the order they
// run in, alongside whatever else a test appends to events.
type regFake struct {
	events                          []string
	bindExports, applyExports       []ecosystem.EnvExport
	bindFail, applyFail, revertFail bool
	setenvErr                       error
	env                             map[string]string
	workDirs                        []string
}

func newRegFake() *regFake { return &regFake{env: map[string]string{}} }

func (g *regFake) deps() registryDeps {
	return registryDeps{
		Gate: func(io.Writer) bindregistry.Gate {
			g.events = append(g.events, "gate")
			return bindregistry.Gate{}
		},
		BindHomes: func(_ io.Writer, _ bindregistry.Gate, publish bindregistry.PublishFunc) bool {
			g.events = append(g.events, "bind")
			publish(g.bindExports)
			return !g.bindFail
		},
		ApplyInTree: func(_ io.Writer, workDir string, _ bindregistry.Gate, publish bindregistry.PublishFunc) bool {
			g.events = append(g.events, "apply")
			g.workDirs = append(g.workDirs, workDir)
			publish(g.applyExports)
			return !g.applyFail
		},
		Revert: func(workDir string, _ io.Writer) bool {
			g.events = append(g.events, "revert")
			g.workDirs = append(g.workDirs, workDir)
			return !g.revertFail
		},
		Setenv: func(k, v string) error {
			g.events = append(g.events, "setenv "+k)
			g.env[k] = v
			return g.setenvErr
		},
	}
}

// registryFixture wires the fakes and records the toolchain probe and the
// Driver run in the same event log.
func registryFixture(t *testing.T) (*fixture, *regFake) {
	f := newFixture(t)
	g := newRegFake()
	f.d.Registry = g.deps()
	f.target("flake.nix")
	f.knobs["DEV_SHELL_PROBE_TIMEOUT"] = "300"
	f.nixAtCall = func() { g.events = append(g.events, "nix") }
	orchestrate := f.d.Orchestrate
	f.d.Orchestrate = func(argv []string) int {
		g.events = append(g.events, "driver")
		return orchestrate(argv)
	}
	return f, g
}

func TestRegistry_BindsBeforeTheToolchainDecisionOnOneGate(t *testing.T) {
	f, g := registryFixture(t)
	f.run()
	want := []string{"gate", "bind", "apply", "nix", "driver", "revert"}
	if !reflect.DeepEqual(g.events, want) {
		t.Fatalf("events = %v, want %v", g.events, want)
	}
	if !reflect.DeepEqual(g.workDirs, []string{f.in.WorkDir, f.in.WorkDir}) {
		t.Errorf("work dirs = %v, want apply and revert on the work dir", g.workDirs)
	}
}

func TestRegistry_SelfContainedSkipsTheInTreeSteps(t *testing.T) {
	f, g := registryFixture(t)
	f.env.SelfContained = true
	f.run()
	want := []string{"gate", "bind", "driver"}
	if !reflect.DeepEqual(g.events, want) {
		t.Fatalf("events = %v, want %v", g.events, want)
	}
}

func TestRegistry_ExportsReachTheEnvironmentBindingsFirst(t *testing.T) {
	f, g := registryFixture(t)
	g.bindExports = []ecosystem.EnvExport{{Name: "GOPROXY", Value: "http://127.0.0.1:1"}, {Name: "GOSUMDB", Value: "off"}}
	g.applyExports = []ecosystem.EnvExport{{Name: "CARGO_X", Value: "placeholder"}}
	f.run()
	want := []string{"gate", "bind", "setenv GOPROXY", "setenv GOSUMDB", "apply", "setenv CARGO_X", "nix", "driver", "revert"}
	if !reflect.DeepEqual(g.events, want) {
		t.Fatalf("events = %v, want %v", g.events, want)
	}
	if g.env["GOPROXY"] != "http://127.0.0.1:1" || g.env["CARGO_X"] != "placeholder" {
		t.Errorf("env = %v", g.env)
	}
}

func TestRegistry_FailedBindWarnsAppliesNothingAndCarriesOn(t *testing.T) {
	f, g := registryFixture(t)
	g.bindFail = true
	g.bindExports = []ecosystem.EnvExport{{Name: "GOPROXY", Value: "x"}}
	g.applyExports = []ecosystem.EnvExport{{Name: "CARGO_X", Value: "y"}}
	if rc := f.run(); rc != 0 {
		t.Fatalf("rc = %d, want the run to carry on", rc)
	}
	if countLine(f.lines(), bindWarning) != 1 {
		t.Errorf("stdout = %q, want %q once", f.lines(), bindWarning)
	}
	if _, ok := g.env["GOPROXY"]; ok {
		t.Errorf("failed bind exported %v", g.env)
	}
	if g.env["CARGO_X"] != "y" {
		t.Errorf("the in-tree step's export was lost: %v", g.env)
	}
}

func TestRegistry_FailedApplyWarnsRevertsAndCarriesOn(t *testing.T) {
	f, g := registryFixture(t)
	g.applyFail = true
	g.bindExports = []ecosystem.EnvExport{{Name: "GOPROXY", Value: "x"}}
	g.applyExports = []ecosystem.EnvExport{{Name: "CARGO_X", Value: "y"}}
	if rc := f.run(); rc != 0 {
		t.Fatalf("rc = %d, want the run to carry on", rc)
	}
	if countLine(f.lines(), applyWarning) != 1 {
		t.Errorf("stdout = %q, want %q once", f.lines(), applyWarning)
	}
	if _, ok := g.env["CARGO_X"]; ok || g.env["GOPROXY"] != "x" {
		t.Errorf("env = %v, want only the bindings' exports", g.env)
	}
	want := []string{"gate", "bind", "setenv GOPROXY", "apply", "revert", "nix", "driver", "revert"}
	if !reflect.DeepEqual(g.events, want) {
		t.Fatalf("events = %v, want %v", g.events, want)
	}
}

func TestRegistry_FailedRevertWarnsWithoutChangingTheExitCode(t *testing.T) {
	f, g := registryFixture(t)
	g.revertFail = true
	f.firstRun(blockedLine+"\n", 6)
	if rc := f.run(); rc != 6 {
		t.Fatalf("rc = %d, want the Driver's 6", rc)
	}
	if countLine(f.lines(), revertWarning) != 1 {
		t.Errorf("stdout = %q, want %q once", f.lines(), revertWarning)
	}
}

func TestRegistry_RevertsAfterTheDriverOnEveryExit(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fixture)
		after string
	}{
		{"success", func(f *fixture) {}, "driver"},
		{"driver failure", func(f *fixture) { f.firstRun(blockedLine+"\n", 6) }, "driver"},
		{"phase error", func(f *fixture) { f.assembleErr = errors.New("boom") }, "nix"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, g := registryFixture(t)
			c.setup(f)
			_, _ = run(f.in, f.env, f.d)
			n := len(g.events)
			if n < 2 || g.events[n-1] != "revert" || g.events[n-2] != c.after {
				t.Fatalf("events = %v, want a final revert right after %q", g.events, c.after)
			}
		})
	}
}

func TestRegistry_RevertsOnTheConflictResolveEarlyExit(t *testing.T) {
	f := newCRFixture(t)
	g := newRegFake()
	f.d.Registry = g.deps()
	if rc := f.run(); rc != 1 {
		t.Fatalf("rc = %d, want 1", rc)
	}
	if n := len(g.events); n == 0 || g.events[n-1] != "revert" {
		t.Fatalf("events = %v, want the run to end on a revert", g.events)
	}
}

func TestRealRegistryDepsSetenvIsTheProcessEnvironment(t *testing.T) {
	t.Setenv("BOX_REGISTRY_TEST", "")
	if err := realRegistryDeps().Setenv("BOX_REGISTRY_TEST", "v"); err != nil || os.Getenv("BOX_REGISTRY_TEST") != "v" {
		t.Fatalf("Setenv did not reach the process env: %v", err)
	}
}

func TestRegistry_FailedSetenvWarnsNamingTheVariableAndCarriesOn(t *testing.T) {
	f, g := registryFixture(t)
	g.setenvErr = errors.New("boom")
	g.bindExports = []ecosystem.EnvExport{{Name: "GOPROXY", Value: "x"}, {Name: "GOSUMDB", Value: "off"}}
	if rc := f.run(); rc != 0 {
		t.Fatalf("rc = %d, want the run to carry on", rc)
	}
	for _, name := range []string{"GOPROXY", "GOSUMDB"} {
		want := "==> WARNING: registry proxy binding " + name + " could not be exported: boom"
		if countLine(f.lines(), want) != 1 {
			t.Errorf("stdout = %q, want %q once", f.lines(), want)
		}
	}
}

func TestSpawnTCPForwarder_ExecsDriverExecFromPath(t *testing.T) {
	bin := t.TempDir()
	argv := filepath.Join(bin, "argv")
	script := "#!/bin/sh\necho \"$*\" >" + argv + "\n"
	if err := os.WriteFile(filepath.Join(bin, "driver-exec"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	pid, err := spawnTCPForwarder("up.example", 8080, "s3cret", 27182)
	if err != nil || pid == 0 {
		t.Fatalf("pid=%d err=%v", pid, err)
	}
	want := "forward-registry-tcp -listen-port 27182 -upstream-host up.example -upstream-port 8080"
	var got []byte
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if got, _ = os.ReadFile(argv); strings.HasSuffix(string(got), "\n") {
			break
		}
	}
	if strings.TrimSpace(string(got)) != want {
		t.Errorf("driver-exec argv = %q, want %q", got, want)
	}
}

func TestSpawnTCPForwarder_MissingDriverExecIsAnError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := spawnTCPForwarder("up.example", 8080, "s", 27182); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("err = %v, want exec.ErrNotFound", err)
	}
}
