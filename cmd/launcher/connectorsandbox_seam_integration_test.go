//go:build integration

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/fixturebuild"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/seambundle"
	"spindrift.dev/launcher/internal/seamtest"
)

// stoppedLine is what the Connector sandbox's stop hook prints; bwrap's kill
// is an in-process signal the fake never sees, so the line is the one
// observable both runner families share.
const stoppedLine = "==> stopped Connector sandbox: "

// sandboxExit is one way a launcher run ends with a Connector sandbox started.
// A run is a work dispatch on the family's fake, with the github backend row
// pointed at a fixture Connector so a Connector load, and with it the sandbox,
// happens.
type sandboxExit struct {
	name string
	// shadow loads the Connector lazily from a read shadow, during the run; a
	// routed row loads it from the launch token gate, before the run starts.
	shadow bool
	// box is the scripted Box run.
	box seamtest.PodmanRun
	// wantExit is the launcher's exit status, and wantOut text its output must
	// carry: the proof the run ended the way the row says.
	wantExit int
	wantOut  string
	// wantBox is whether a Box is expected to have run.
	wantBox bool
}

// sandboxExits is every exit path the seam harness drives. A path that starts
// the sandbox another way, or has the launcher end differently, is one more row.
var sandboxExits = []sandboxExit{
	{name: "success", shadow: true, box: seamtest.PodmanRun{Outcome: seamOutcome("ready")}, wantBox: true, wantOut: "verified-merged"},
	{name: "failed-run", shadow: true, box: seamtest.PodmanRun{Exit: 1}, wantBox: true, wantOut: "(box-failed)"},
	{name: "launcher-error", shadow: false, wantExit: 1, wantOut: "forge.tokenGrant"},
}

func seamOutcome(status string) string {
	return "SPINDRIFT_OUTCOME issue=7 landing=" + smokePR + " status=" + status + " note=seam\n"
}

func TestSeamConnectorSandboxStoppedOnce(t *testing.T) {
	for _, runtime := range []string{"podman", "bwrap"} {
		for _, x := range sandboxExits {
			t.Run(x.name+"-"+runtime, func(t *testing.T) { runSandboxExit(t, runtime, x) })
		}
	}
}

func runSandboxExit(t *testing.T, runtime string, x sandboxExit) {
	t.Helper()
	fixture := "launcher-run-input.json"
	if runtime == "bwrap" {
		fixture = "launcher-run-input-bwrap.json"
	}
	connectors, err := fixturebuild.ConnectorDir()
	if err != nil {
		t.Fatal(err)
	}
	host, err := fixturebuild.ConnectorHost()
	if err != nil {
		t.Fatal(err)
	}
	docPath := sandboxDoc(t, seamtest.Path(t, fixture), filepath.Join(connectors, "forge.wasm"), x.shadow)

	bin := seamtest.BuildLauncher(t)
	fakeDir := seamtest.InstallFakes(t, "podman", "docker", "bwrap", "gh")
	recDir := t.TempDir()
	workDir := t.TempDir()

	issue := seamtest.GhIssue{Number: 7, Title: "T", Body: "B", Labels: []string{"ready-for-agent"}}
	env := seamtest.WriteFakeConfig(t, "gh", seamtest.GhConfig{
		Record:  recDir + "/gh.jsonl",
		Issues:  []seamtest.GhIssue{issue},
		Replies: []seamtest.GhReply{{Args: []string{"pr", "create"}, Stdout: smokePR + "\n"}},
		PRs: []seamtest.GhPR{{Number: 9, URL: smokePR, HeadRefName: "agent/issue-7", BaseRefName: "main",
			HeadRefOid: "abc123", Checks: "SUCCESS", Mergeable: "MERGEABLE"}},
	})
	bare, gitEnv := seedLedgerRemote(t)
	for k, v := range gitEnv {
		env[k] = v
	}
	run := x.box
	run.Files = map[string]string{
		filepath.Join(hostpaths.OutboxDir(workDir, "7"), seambundle.FileName): seamBundle(t, bare),
	}
	runs := []seamtest.PodmanRun{run}
	for _, tool := range []string{"podman", "docker"} {
		cfg := seamtest.PodmanConfig{Record: recDir + "/" + tool + ".jsonl", ImagePresent: true, ConnectorHost: host}
		if tool == runtime {
			cfg.Runs = runs
		}
		for k, v := range seamtest.WriteFakeConfig(t, tool, cfg) {
			env[k] = v
		}
	}
	// bwrap runs with an allowlisted env, so its config travels by cwd.
	seamtest.WriteFakeConfigFile(t, workDir, "bwrap", seamtest.BwrapConfig{Record: recDir + "/bwrap.jsonl", Runs: runs, ConnectorHost: host})
	for k, v := range smokeSecrets {
		env[k] = v
	}

	args := []string{"--input", docPath, "--repo-slug", smokeRepo, "--box-signal-carrier", "log",
		"--user-name", "Seam Bot", "--user-email", "seam@example.com", "--no-build",
		"--merge-policy", "immediate", "--merge-poll-interval", "1", "dispatch"}
	if runtime == "bwrap" {
		args = append([]string{"--network-mode", "host"}, args...)
	}
	res := seamtest.Run(t, seamtest.Cmd{Bin: bin, Args: args, Env: env, CleanEnv: true, PathDirs: []string{fakeDir}, Dir: workDir})
	res.WantExit(t, x.wantExit)
	if out := res.Stdout + res.Stderr; !strings.Contains(out, x.wantOut) {
		t.Errorf("output lacks %q:\n%s", x.wantOut, out)
	}

	var stopped []string
	for _, line := range strings.Split(res.Stderr, "\n") {
		if name, ok := strings.CutPrefix(line, stoppedLine); ok {
			stopped = append(stopped, name)
		}
	}
	if len(stopped) != 1 {
		t.Fatalf("the stop hook ran %d times (%q), want exactly once\nstderr:\n%s", len(stopped), stopped, res.Stderr)
	}

	recs := seamtest.ReadRecord(t, recDir+"/"+runtime+".jsonl")
	starts, boxes := 0, 0
	for _, r := range recs {
		switch {
		case runtime == "bwrap" && strings.HasSuffix(r[len(r)-1], "/bin/connector-host"):
			starts++
		case runtime == "bwrap":
			boxes++
		case r[0] == "run" && sandboxName(r) != "":
			starts++
			if got := sandboxName(r); got != stopped[0] {
				t.Errorf("stopped sandbox %q, but the runtime started %q", stopped[0], got)
			}
		case r[0] == "run":
			boxes++
		}
	}
	if starts != 1 {
		t.Errorf("the runtime started %d Connector sandboxes, want 1", starts)
	}
	if want := b2i(x.wantBox); boxes != want {
		t.Errorf("the runtime ran %d Boxes, want %d", boxes, want)
	}
	if runtime == "podman" {
		// The OCI stop hook probes the container before removing it, and the
		// fake reports none, so one probe per sandbox name is one hook call.
		probes := 0
		for _, r := range recs {
			if r[0] == "inspect" && r[len(r)-1] == stopped[0] {
				probes++
			}
		}
		if probes != 1 {
			t.Errorf("podman probed sandbox %s %d times, want once", stopped[0], probes)
		}
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// sandboxName is the --name of a sandbox `run` argv, "" for any other.
func sandboxName(argv []string) string {
	for i, a := range argv {
		if n, ok := strings.CutPrefix(a, "--name="); ok && strings.HasPrefix(n, runner.ConnectorSandboxPrefix) {
			return n
		}
		if a == "--name" && i+1 < len(argv) && strings.HasPrefix(argv[i+1], runner.ConnectorSandboxPrefix) {
			return argv[i+1]
		}
	}
	return ""
}

// sandboxDoc writes a copy of the input document at docPath with CONNECTOR_SANDBOX
// on and a github backend row whose Connector is the wasm at module. The row
// reads through GH_TOKEN. As a shadow row it loads on the run's first read, and
// native stays authoritative; as a routed one the launch token gate loads it,
// and its lack of a token grant refuses the launch.
func sandboxDoc(t *testing.T, docPath, module string, shadow bool) string {
	t.Helper()
	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	var settings map[string]string
	if err := json.Unmarshal(doc["settings"], &settings); err != nil {
		t.Fatal(err)
	}
	settings["CONNECTOR_SANDBOX"] = "true"
	row := map[string]any{
		"name": "github", "module": module, "credentialEnv": "GH_TOKEN",
		"routes": []map[string]string{{"method": "GET", "host": "api.github.com", "path": "/repos/{repo}"}},
	}
	if shadow {
		row["shadow"], row["shadowEvery"] = true, 1
	}
	for k, v := range map[string]any{"settings": settings, "backends": []any{row}} {
		if doc[k], err = json.Marshal(v); err != nil {
			t.Fatal(err)
		}
	}
	if data, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "sandbox-input.json")
	if err := os.WriteFile(scratch, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return scratch
}
