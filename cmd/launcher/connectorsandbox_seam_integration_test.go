//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/fixturebuild"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
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
	// issues are the ready issues the tracker holds, default just #7; the first
	// one dispatched is the one the Box run answers. maxParallel, when set,
	// caps concurrent Boxes.
	issues      []int
	maxParallel int
	// signal, when set, is sent to the launcher once the Box is in flight; the
	// Box stays held until the launcher has latched the stop it asks for.
	signal os.Signal
}

// sandboxExits is every exit path the seam harness drives. A path that starts
// the sandbox another way, or has the launcher end differently, is one more row.
var sandboxExits = []sandboxExit{
	{name: "success", shadow: true, box: seamtest.PodmanRun{Outcome: seamOutcome("ready")}, wantBox: true, wantOut: "verified-merged"},
	{name: "failed-run", shadow: true, box: seamtest.PodmanRun{Exit: 1}, wantBox: true, wantOut: "(box-failed)"},
	{name: "launcher-error", shadow: false, wantExit: 1, wantOut: "forge.tokenGrant"},
	{name: "blocked-run", shadow: true, box: seamtest.PodmanRun{Outcome: seamOutcome("blocked")}, wantBox: true, wantOut: "(blocked)"},
	{name: "sigint", shadow: true, box: seamtest.PodmanRun{Outcome: seamOutcome("ready")}, signal: syscall.SIGINT, wantExit: 7, wantBox: true, wantOut: "verified-merged"},
	{name: "sigterm", shadow: true, box: seamtest.PodmanRun{Outcome: seamOutcome("ready")}, signal: syscall.SIGTERM, wantExit: 7, wantBox: true, wantOut: "verified-merged"},
	// The shutdown gate declines the issue left waiting on the one Box slot
	// while the stop arrives, so no second Box launches. Which issue goes first
	// is unordered, so the Box run may answer the other; the gate's own line is
	// the proof it declined.
	{name: "shutdown-gate", shadow: true, box: seamtest.PodmanRun{Outcome: seamOutcome("ready")}, issues: []int{7, 8}, maxParallel: 1, signal: syscall.SIGTERM, wantExit: 7, wantBox: true, wantOut: "==> stop requested; nothing in flight"},
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

// sandboxRig is a launcher run wired to the family's fakes and ready to start.
type sandboxRig struct {
	cmd    seamtest.Cmd
	recDir string
	// release frees every Box scripted with HoldUntil.
	release string
	// The rest is what a daemon rig needs to run the same launcher as its child:
	// the launcher binary and its flags (without the verb), the input document,
	// the working directory and the fakes' environment.
	bin, docPath, workDir, fakeDir string
	flags                          []string
	env                            map[string]string
}

// sandboxRigOpts is what newSandboxRig varies; a struct so the bools cannot be
// swapped silently (issue #3060).
type sandboxRigOpts struct {
	// shadow loads the Connector from a read shadow rather than a routed row.
	shadow bool
	// runs script the Boxes in the order they start; running names the
	// containers the podman fake reports as already running; imageAbsent makes
	// the OCI fakes report the Box image as not loaded.
	runs        []seamtest.PodmanRun
	issues      []int
	running     []string
	imageAbsent bool
	// extra are launcher flags appended to the rig's.
	extra []string
}

// newSandboxRig builds a work dispatch of o.issues on runtime's fakes. The rig
// releases a held Box through release.
func newSandboxRig(t *testing.T, runtime string, o sandboxRigOpts) sandboxRig {
	t.Helper()
	shadow, issues, imageAbsent, running := o.shadow, o.issues, o.imageAbsent, o.running
	connectors, err := fixturebuild.ConnectorDir()
	if err != nil {
		t.Fatal(err)
	}
	host, err := fixturebuild.ConnectorHost()
	if err != nil {
		t.Fatal(err)
	}
	fixture, artifacts := "launcher-run-input.json", map[string]string(nil)
	if runtime == "bwrap" {
		fixture, artifacts = "launcher-run-input-bwrap.json", bwrapSandboxArtifacts(t, host)
	}
	docPath := sandboxDoc(t, seamtest.Path(t, fixture), filepath.Join(connectors, "forge.wasm"), shadow, artifacts)

	bin := seamtest.BuildLauncher(t)
	fakeDir := seamtest.InstallFakes(t, "podman", "docker", "bwrap", "gh")
	recDir := t.TempDir()
	workDir := t.TempDir()

	var ghIssues []seamtest.GhIssue
	for _, n := range issues {
		ghIssues = append(ghIssues, seamtest.GhIssue{Number: n, Title: "T", Body: "B", Labels: []string{"ready-for-agent"}})
	}
	env := seamtest.WriteFakeConfig(t, "gh", seamtest.GhConfig{
		Record:  recDir + "/gh.jsonl",
		Issues:  ghIssues,
		Replies: []seamtest.GhReply{{Args: []string{"pr", "create"}, Stdout: smokePR + "\n"}},
		PRs: []seamtest.GhPR{{Number: 9, URL: smokePR, HeadRefName: "agent/issue-7", BaseRefName: "main",
			HeadRefOid: "abc123", Checks: "SUCCESS", Mergeable: "MERGEABLE"}},
	})
	bare, gitEnv := seedLedgerRemote(t)
	for k, v := range gitEnv {
		env[k] = v
	}
	release := filepath.Join(recDir, "release")
	runs := slices.Clone(o.runs)
	for i := range runs {
		if runs[i].HoldUntil != "" {
			runs[i].HoldUntil = release
		}
		runs[i].Files = map[string]string{
			filepath.Join(hostpaths.OutboxDir(workDir, "7"), seambundle.FileName): seamBundle(t, bare),
		}
	}
	for _, tool := range []string{"podman", "docker"} {
		cfg := seamtest.PodmanConfig{Record: recDir + "/" + tool + ".jsonl", ImagePresent: !imageAbsent, ConnectorHost: host}
		if tool == runtime {
			cfg.Runs, cfg.Running = runs, running
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
		"--merge-policy", "immediate", "--merge-poll-interval", "1"}
	if runtime == "bwrap" {
		args = append([]string{"--network-mode", "host"}, args...)
	}
	flags := append(args, o.extra...)
	return sandboxRig{
		cmd:    seamtest.Cmd{Bin: bin, Args: append(slices.Clone(flags), "dispatch"), Env: env, CleanEnv: true, PathDirs: []string{fakeDir}, Dir: workDir},
		recDir: recDir, release: release,
		bin: bin, docPath: docPath, workDir: workDir, fakeDir: fakeDir, flags: flags, env: env,
	}
}

func runSandboxExit(t *testing.T, runtime string, x sandboxExit) {
	t.Helper()
	issues := x.issues
	if issues == nil {
		issues = []int{7}
	}
	var extra []string
	if x.maxParallel > 0 {
		extra = []string{"--max-parallel", strconv.Itoa(x.maxParallel)}
	}
	run := x.box
	if x.signal != nil {
		run.HoldUntil = "set by the rig"
	}
	rig := newSandboxRig(t, runtime, sandboxRigOpts{shadow: x.shadow, runs: []seamtest.PodmanRun{run}, issues: issues, extra: extra})
	recPath := filepath.Join(rig.recDir, runtime+".jsonl")

	p := seamtest.Start(t, rig.cmd)
	if x.signal != nil {
		// Whatever happens, free a held fake Box so it cannot outlive the test.
		t.Cleanup(func() { _ = os.WriteFile(rig.release, nil, 0o644) })
		runnertest.Eventually(t, time.Minute, "a Box to start", func() bool {
			_, boxes := countRuns(runtime, seamtest.ReadRecord(t, recPath))
			return boxes > 0
		})
		p.Signal(t, x.signal)
		awaitStopLatched()
		if err := os.WriteFile(rig.release, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := p.Wait(t)
	res.WantExit(t, x.wantExit)
	if out := res.Stdout + res.Stderr; !strings.Contains(out, x.wantOut) {
		t.Errorf("output lacks %q:\n%s", x.wantOut, out)
	}

	// The stopped line prints only after a successful stop, so a fail-then-retry
	// still shows one; no failed-attempt warning too is what proves one hook run.
	stopped := stoppedSandboxes(res.Stderr)
	if failed := strings.Count(res.Stderr, "warning: stopping Connector sandbox"); len(stopped) != 1 || failed != 0 {
		t.Fatalf("the stop hook ran %d times (%q) with %d failed attempts, want exactly once\nstderr:\n%s", len(stopped), stopped, failed, res.Stderr)
	}

	recs := seamtest.ReadRecord(t, recPath)
	starts, boxes := countRuns(runtime, recs)
	for _, r := range recs {
		if got := sandboxName(r); runtime == "podman" && r[0] == "run" && got != "" && got != stopped[0] {
			t.Errorf("stopped sandbox %q, but the runtime started %q", stopped[0], got)
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

// TestSeamConnectorSandboxReapsOrphans starts a launcher while the runtime
// still lists a sandbox a hard-killed launcher left behind, and asserts it
// removes that one and nothing that merely resembles it, whether or not the
// Box image is loaded. Only podman has a seam to stage a leftover: bwrap lists
// cgroup directories under the host's real cgroupfs, which a launcher
// subprocess cannot be pointed away from, so bwrap's list-then-stop is pinned
// at the runner (runnertest SandboxOrphan).
func TestSeamConnectorSandboxReapsOrphans(t *testing.T) {
	orphan, others := stageOrphan(t)
	for _, imageAbsent := range []bool{false, true} {
		t.Run(fmt.Sprintf("imageAbsent=%t", imageAbsent), func(t *testing.T) {
			rig := newSandboxRig(t, "podman", sandboxRigOpts{
				shadow: true, runs: []seamtest.PodmanRun{{Outcome: seamOutcome("ready")}}, issues: []int{7},
				running: append([]string{orphan}, others...), imageAbsent: imageAbsent,
			})
			res := seamtest.Start(t, rig.cmd).Wait(t)
			if imageAbsent {
				// The sweep needs no image, so it lands even though the launcher
				// then refuses to start without one.
				if res.ExitCode == 0 {
					t.Fatalf("exit code = 0 with the image absent; want non-zero\nstderr:\n%s", res.Stderr)
				}
				if out := res.Stdout + res.Stderr; !strings.Contains(out, imageAbsentRefusal) {
					t.Errorf("output lacks the image-absent refusal %q:\n%s", imageAbsentRefusal, out)
				}
			} else {
				res.WantExit(t, 0)
			}

			var reaped []string
			for _, line := range strings.Split(res.Stderr, "\n") {
				if name, ok := strings.CutPrefix(line, "==> reaped orphaned Connector sandbox: "); ok {
					reaped = append(reaped, name)
				}
			}
			if len(reaped) != 1 || reaped[0] != orphan {
				t.Errorf("reaped %q, want only %q", reaped, orphan)
			}
			assertOrphanReaped(t, seamtest.ReadRecord(t, filepath.Join(rig.recDir, "podman.jsonl")), orphan, others)
		})
	}
}

// imageAbsentRefusal is the readiness error the OCI runner returns when the Box
// image is not loaded.
const imageAbsentRefusal = "image absent; run `spindrift build`"

// stageOrphan is the name of a sandbox a hard-killed launcher left behind (its
// owner pid is dead) and the running names a reap must leave alone: a sandbox
// whose launcher still runs (this test process), a real Box, and names that only
// resemble a sandbox's.
func stageOrphan(t *testing.T) (orphan string, others []string) {
	t.Helper()
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	orphan = fmt.Sprintf("%s%d-1700000000000000000", runner.ConnectorSandboxPrefix, dead.Process.Pid)
	others = []string{
		fmt.Sprintf("%s%d-1700000000000000000", runner.ConnectorSandboxPrefix, os.Getpid()),
		dispatch.BoxName("99"),
		"spindrift-box-connector-sandbox-7",
		fmt.Sprintf("box-%s%d-1700000000000000000", runner.ConnectorSandboxPrefix, dead.Process.Pid),
	}
	return orphan, others
}

// assertOrphanReaped fails unless the runtime record shows the orphan removed
// exactly once and no argv naming any of others.
func assertOrphanReaped(t *testing.T, recs [][]string, orphan string, others []string) {
	t.Helper()
	for _, r := range recs {
		line := strings.Join(r, " ")
		for _, o := range others {
			if strings.Contains(line, o) {
				t.Errorf("touched %s: podman %s", o, line)
			}
		}
	}
	if got := countArgv(recs, "rm", "-f", "id-"+orphan); got != 1 {
		t.Errorf("podman removed the orphan %d times, want once", got)
	}
}

// awaitStopLatched gives a signalled launcher time to latch the stop. Its
// dispatch gate observes the stop only at its next Launch or Settle and prints
// nothing while a Box is in flight, so there is no trace to poll; releasing the
// Box early would let the run end unsignalled.
func awaitStopLatched() { time.Sleep(300 * time.Millisecond) }

func countArgv(recs [][]string, want ...string) int {
	n := 0
	for _, r := range recs {
		if slices.Equal(r, want) {
			n++
		}
	}
	return n
}

// stoppedSandboxes is the sandbox names a launcher's stderr reports stopped.
func stoppedSandboxes(stderr string) []string {
	var names []string
	for _, line := range strings.Split(stderr, "\n") {
		if name, ok := strings.CutPrefix(line, stoppedLine); ok {
			names = append(names, name)
		}
	}
	return names
}

// countRuns is how many Connector sandboxes and how many Boxes the runtime's
// record shows started.
func countRuns(runtime string, recs [][]string) (sandboxes, boxes int) {
	for _, r := range recs {
		switch {
		case runtime == "bwrap" && seamtest.IsBwrapOverlayProbe(r):
		case runtime == "bwrap" && strings.HasSuffix(r[len(r)-1], "/bin/connector-host"):
			sandboxes++
		case runtime == "bwrap":
			boxes++
		case r[0] == "run" && sandboxName(r) != "":
			sandboxes++
		case r[0] == "run":
			boxes++
		}
	}
	return sandboxes, boxes
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
// and its lack of a token grant refuses the launch. artifacts overrides keys of
// the document's artifacts.
func sandboxDoc(t *testing.T, docPath, module string, shadow bool, artifacts map[string]string) string {
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
	if len(artifacts) > 0 {
		var art map[string]string
		if err := json.Unmarshal(doc["artifacts"], &art); err != nil {
			t.Fatal(err)
		}
		maps.Copy(art, artifacts)
		if doc["artifacts"], err = json.Marshal(art); err != nil {
			t.Fatal(err)
		}
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

// bwrapSandboxArtifacts points the bwrap fixture's connector-host and syscall
// filter at local files. The fixture names absent sentinel store paths, in a
// Box as in CI, and a missing connector-host sends the launcher to `nix build`
// its sentinel drv.
func bwrapSandboxArtifacts(t *testing.T, host string) map[string]string {
	t.Helper()
	pkg := t.TempDir()
	if err := os.Mkdir(filepath.Join(pkg, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(host, filepath.Join(pkg, "bin", "connector-host")); err != nil {
		t.Fatal(err)
	}
	filter := filepath.Join(t.TempDir(), "syscall-filter.bpf")
	if err := os.WriteFile(filter, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"CONNECTOR_HOST": pkg, "SYSCALL_FILTER": filter}
}
