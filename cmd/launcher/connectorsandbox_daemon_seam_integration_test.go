//go:build integration

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/runner/runnertest"
	"spindrift.dev/launcher/internal/seamtest"
)

// daemonSlots is how many Launcher runs the daemon holds at once.
const daemonSlots = 2

// newDaemonRig is the sandbox rig's launcher run as a daemon's child: the
// daemon binary supervises daemonSlots children in a git checkout of the fake
// remote, and the nix fake turns each child's `nix run` into the real launcher
// with the rig's flags. Every Box is scripted to hold until the rig's release.
func newDaemonRig(t *testing.T, runtime string) sandboxRig {
	t.Helper()
	box := seamtest.PodmanRun{Outcome: seamOutcome("blocked"), HoldUntil: "set by the rig"}
	rig := newSandboxRig(t, runtime, true, []seamtest.PodmanRun{box, box}, []int{7, 8}, nil)

	setDocSettings(t, rig.docPath, map[string]string{
		"REPO_SLUG": smokeRepo, "MAX_PARALLEL": "2", "DAEMON_IDLE_FLOOR": "1s", "DAEMON_IDLE_CAP": "2s",
	})
	gitEnv := append(os.Environ(), "GIT_CONFIG_GLOBAL="+rig.env["GIT_CONFIG_GLOBAL"])
	seamGit(t, gitEnv, rig.workDir, "init", "-q", "-b", "main")
	seamGit(t, gitEnv, rig.workDir, "remote", "add", "origin", "https://github.com/"+smokeRepo+".git")

	for k, v := range seamtest.WriteFakeConfig(t, "nix", seamtest.NixConfig{
		Record: filepath.Join(rig.recDir, "nix.jsonl"), Exec: rig.bin, ExecArgs: rig.flags,
	}) {
		rig.env[k] = v
	}
	rig.cmd = seamtest.Cmd{
		Bin: seamtest.Build(t, "./daemon"), Args: []string{"--input", rig.docPath, "dispatch"},
		Env: rig.env, CleanEnv: true, Dir: rig.workDir,
		PathDirs: []string{rig.fakeDir, seamtest.InstallFakes(t, "nix")},
	}
	return rig
}

// daemonChildLogs is the Child log of each child the daemon's event stream
// reports started, as paths in the checkout.
func daemonChildLogs(t *testing.T, workDir, stdout string) []string {
	t.Helper()
	var logs []string
	for _, line := range strings.Split(stdout, "\n") {
		var ev struct {
			Event    string `json:"event"`
			ChildLog string `json:"child_log"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Event == "child_start" {
			logs = append(logs, filepath.Join(workDir, ev.ChildLog))
		}
	}
	return logs
}

func countEvents(stdout, event string) int {
	return strings.Count(stdout, `"event":"`+event+`"`)
}

// TestSeamDaemonRunsEachHoldOneSandbox drives a daemon with two runs in flight
// at once. Each run is its own launcher process, so each holds one Connector
// sandbox, and the sandbox is stopped once, with the run, however the daemon
// ends: when the runs settle on their own, or when the daemon is stopped with
// both still in flight.
func TestSeamDaemonRunsEachHoldOneSandbox(t *testing.T) {
	for _, runtime := range []string{"podman", "bwrap"} {
		for _, stopInFlight := range []bool{false, true} {
			name := "settle-" + runtime
			if stopInFlight {
				name = "shutdown-" + runtime
			}
			t.Run(name, func(t *testing.T) { runDaemonSandboxes(t, runtime, stopInFlight) })
		}
	}
}

func runDaemonSandboxes(t *testing.T, runtime string, stopInFlight bool) {
	rig := newDaemonRig(t, runtime)
	recPath := rig.recDir + "/" + runtime + ".jsonl"
	t.Cleanup(func() { _ = os.WriteFile(rig.release, nil, 0o644) })

	p := seamtest.Start(t, rig.cmd)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("daemon stdout:\n%s\ndaemon stderr:\n%s", p.Stdout(), p.Stderr())
		}
	})
	runnertest.Eventually(t, time.Minute, "both runs' Boxes to be in flight", func() bool {
		_, boxes := countRuns(runtime, seamtest.ReadRecord(t, recPath))
		return boxes >= daemonSlots
	})
	// Both runs are held mid-run, so each owns its sandbox and no more.
	if starts, _ := countRuns(runtime, seamtest.ReadRecord(t, recPath)); starts != daemonSlots {
		t.Errorf("%d Connector sandboxes with %d runs in flight, want one each", starts, daemonSlots)
	}
	for _, log := range daemonChildLogs(t, rig.workDir, p.Stdout()) {
		if got := stoppedSandboxes(readFile(t, log)); len(got) != 0 {
			t.Errorf("%s stopped %q while its run was in flight", log, got)
		}
	}

	if stopInFlight {
		p.Signal(t, syscall.SIGTERM)
		runnertest.Eventually(t, time.Minute, "the daemon to forward the stop", func() bool { return countEvents(p.Stdout(), "shutdown") > 0 })
		// The children latch the forwarded stop silently, so give them time to
		// before the Boxes are released, or a run could end unsignalled.
		time.Sleep(300 * time.Millisecond)
	}
	if err := os.WriteFile(rig.release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !stopInFlight {
		runnertest.Eventually(t, time.Minute, "both runs to settle", func() bool { return countEvents(p.Stdout(), "child_finish") >= daemonSlots })
		p.Signal(t, syscall.SIGTERM)
	}
	res := p.Wait(t)
	res.WantExit(t, 0)
	if got := strings.Count(res.Stdout, `"outcome":"signalled-stop"`); stopInFlight && got != daemonSlots {
		t.Errorf("%d runs ended on the daemon's stop, want %d", got, daemonSlots)
	}

	recs := seamtest.ReadRecord(t, recPath)
	starts, _ := countRuns(runtime, recs)
	logs := daemonChildLogs(t, rig.workDir, res.Stdout)
	var stopped []string
	for _, log := range logs {
		got := stoppedSandboxes(readFile(t, log))
		if len(got) != 1 {
			t.Errorf("%s: the stop hook ran %d times (%q), want exactly once", log, len(got), got)
		}
		stopped = append(stopped, got...)
	}
	if starts != len(logs) || len(stopped) != starts {
		t.Errorf("%d children started %d Connector sandboxes and stopped %d, want one each", len(logs), starts, len(stopped))
	}
	seen := map[string]bool{}
	for _, name := range stopped {
		if seen[name] {
			t.Errorf("sandbox %s stopped by more than one run", name)
		}
		seen[name] = true
		if runtime != "podman" {
			continue
		}
		probes, started := 0, 0
		for _, r := range recs {
			if r[0] == "inspect" && r[len(r)-1] == name {
				probes++
			}
			if r[0] == "run" && sandboxName(r) == name {
				started++
			}
		}
		if probes != 1 || started != 1 {
			t.Errorf("podman started sandbox %s %d times and probed it %d times, want once each", name, started, probes)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// setDocSettings overrides settings keys of the input document at path.
func setDocSettings(t *testing.T, path string, set map[string]string) {
	t.Helper()
	var doc map[string]json.RawMessage
	var settings map[string]string
	if err := json.Unmarshal([]byte(readFile(t, path)), &doc); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(doc["settings"], &settings); err != nil {
		t.Fatal(err)
	}
	for k, v := range set {
		settings[k] = v
	}
	var err error
	if doc["settings"], err = json.Marshal(settings); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
