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

	"spindrift.dev/launcher/internal/connectorsandbox/sandbox"
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
// A run is a work dispatch on the family's fake; the sandbox starts for the
// Driver's Connector, or for the github row's, which is why every run has one.
type sandboxExit struct {
	name string
	// mode wires the github row. Local runs on a local tracker and the git forge,
	// with no github row, so the run proceeds; the zero mode is a routed fixture
	// that is no tracker, and the run's first read is refused. Tracker is the
	// compiled github Connector in the sandbox, against a fake backend.
	mode sandboxMode
	// box is the scripted Box run.
	box seamtest.PodmanRun
	// wantExit is the launcher's exit status, and wantOut text its output must
	// carry: the proof the run ended the way the row says.
	wantExit int
	wantOut  string
	// wantLabel, in tracker mode, is the label the run's settle must have added
	// to #7 through the sandbox.
	wantLabel string
	// wantBox is whether a Box is expected to have run.
	wantBox bool
	// issues are the ready issues the tracker holds, default just #7; the first
	// one dispatched is the one the Box run answers. maxParallel, when set,
	// caps concurrent Boxes.
	issues      []int
	maxParallel int
	// signal, when set, is sent to the launcher once the Box is in flight; the
	// Box stays held until the launcher has latched the stop it asks for.
	signal syscall.Signal
	// group delivers signal to the launcher's whole process group, as a terminal
	// does for Ctrl-C, so it reaches the Box and sandbox clients that share the
	// group too.
	group bool
}

// sandboxExits is every exit path the seam harness drives. A path that starts
// the sandbox another way, or has the launcher end differently, is one more row.
var sandboxExits = []sandboxExit{
	{name: "success", mode: sandboxLocal, box: seamtest.PodmanRun{Outcome: seamOutcome("ready")}, wantBox: true, wantOut: "branches pushed on owner/repo"},
	{name: "failed-run", mode: sandboxLocal, box: seamtest.PodmanRun{Exit: 1}, wantBox: true, wantOut: "(box-failed)"},
	{name: "failed-run-tracker", mode: sandboxTracker, box: seamtest.PodmanRun{Exit: 1}, wantBox: true, wantOut: "(box-failed)", wantLabel: "agent-failed"},
	{name: "launcher-error", wantExit: 1, wantOut: "cannot list issues"},
	{name: "blocked-run", mode: sandboxLocal, box: seamtest.PodmanRun{Outcome: seamOutcome("blocked")}, wantBox: true, wantOut: "(blocked)"},
	{name: "sigint", mode: sandboxTracker, wantLabel: "agent-complete", box: seamtest.PodmanRun{Outcome: seamOutcome("ready")}, signal: syscall.SIGINT, wantExit: 7, wantBox: true, wantOut: "<- #7 done"},
	{name: "sigterm", mode: sandboxTracker, wantLabel: "agent-complete", box: seamtest.PodmanRun{Outcome: seamOutcome("ready")}, signal: syscall.SIGTERM, wantExit: 7, wantBox: true, wantOut: "<- #7 done"},
	// The terminal's Ctrl-C also kills the Box client, so the run settles
	// failed. Were the sandbox client in the group, the Connector host would die
	// with it before that settle; the process-group assertion is the proof it
	// did not (issue #7302).
	{name: "sigint-group", mode: sandboxTracker, wantLabel: "agent-failed", box: seamtest.PodmanRun{Outcome: seamOutcome("ready")}, signal: syscall.SIGINT, group: true, wantExit: 7, wantBox: true, wantOut: "#7: signal: interrupt"},
	// The shutdown gate declines the issue left waiting on the one Box slot
	// while the stop arrives, so no second Box launches. Which issue goes first
	// is unordered, so the Box run may answer the other; the gate's own line is
	// the proof it declined.
	{name: "shutdown-gate", mode: sandboxLocal, box: seamtest.PodmanRun{Outcome: seamOutcome("ready")}, issues: []int{7, 8}, maxParallel: 1, signal: syscall.SIGTERM, wantExit: 7, wantBox: true, wantOut: "==> stop requested; nothing in flight"},
}

func seamOutcome(status string) string {
	return "SPINDRIFT_OUTCOME issue=7 landing=" + smokePR + " status=" + status + " note=seam\n"
}

func TestSeamConnectorSandboxStoppedOnce(t *testing.T) {
	t.Parallel()
	for _, runtime := range []string{"podman", "bwrap"} {
		for _, x := range sandboxExits {
			t.Run(x.name+"-"+runtime, func(t *testing.T) {
				t.Parallel()
				runSandboxExit(t, runtime, x)
			})
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
	// backend is the fake github the tracker mode's row routes to, nil in any
	// other mode.
	backend *fakeGithub
}

// sandboxMode is how the rig's github backend row, if any, is wired; the modes exclude
// each other.
type sandboxMode int

const (
	// sandboxRouted is a routed row, loaded at launch.
	sandboxRouted sandboxMode = iota
	// sandboxPrimary makes the row the primary read path; like a routed row it
	// loads at launch.
	sandboxPrimary
	// sandboxLocal has no github row: the run reads a local issue and lands by
	// plain git push, so the only Connector it loads is the Driver's.
	sandboxLocal
	// sandboxTracker is the compiled github Connector as the primary tracker,
	// routed to a fake backend, with landing by plain git push. Every tracker
	// read and write, the run's settle included, goes through the sandbox, so
	// a dead sandbox fails the settle.
	sandboxTracker
)

// sandboxRigOpts is what newSandboxRig varies; a struct so its fields cannot be
// swapped silently (issue #3060).
type sandboxRigOpts struct {
	mode sandboxMode
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
	issues, imageAbsent, running := o.issues, o.imageAbsent, o.running
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
	row := map[string]any{
		"name": "github", "module": filepath.Join(connectors, "forge.wasm"), "credentialEnv": "GH_TOKEN",
		"routes": []map[string]string{{"method": "GET", "host": "api.github.com", "path": "/repos/{repo}"}},
	}
	var backend *fakeGithub
	if o.mode == sandboxTracker {
		backend = startFakeGithub(t)
		row = compiledGithubRow(t, backend.srv.Listener.Addr().String())
	}
	docPath := sandboxDoc(t, seamtest.Path(t, fixture), row, o.mode, artifacts)

	bin := seamtest.BuildLauncher(t)
	fakeDir := seamtest.InstallFakes(t, "podman", "docker", "bwrap", "gh")
	recDir := t.TempDir()
	workDir := t.TempDir()

	env := seamtest.WriteFakeConfig(t, "gh", seamtest.GhConfig{Record: recDir + "/gh.jsonl"})
	bare, gitEnv := seedLedgerRemote(t)
	for k, v := range gitEnv {
		env[k] = v
	}
	switch o.mode {
	case sandboxLocal:
		useLocalForge(t, docPath, workDir, bare, issues, "ready-for-agent")
	case sandboxTracker:
		setDocSettings(t, docPath, map[string]string{"CODE_FORGE": "git", "CODE_FORGE_REMOTE_URL": bare})
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
	if backend != nil {
		// The github Connector's HTTP leaves the launcher, which trusts only the
		// fake's certificate; ghs_ makes the write gate skip token introspection.
		env["SSL_CERT_FILE"] = backend.certFile
		env["GH_TOKEN"] = "ghs_seam"
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
		backend: backend,
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
	if x.signal != 0 {
		run.HoldUntil = "set by the rig"
	}
	rig := newSandboxRig(t, runtime, sandboxRigOpts{mode: x.mode, runs: []seamtest.PodmanRun{run}, issues: issues, extra: extra})
	recPath := filepath.Join(rig.recDir, runtime+".jsonl")

	rig.cmd.OwnGroup = x.group
	p := seamtest.Start(t, rig.cmd)
	if x.signal != 0 {
		// Whatever happens, free a held fake Box so it cannot outlive the test.
		t.Cleanup(func() { _ = os.WriteFile(rig.release, nil, 0o644) })
		runnertest.Eventually(t, time.Minute, "a Box to start", func() bool {
			_, boxes := countRuns(runtime, seamtest.ReadRecord(t, recPath))
			return boxes > 0
		})
		if x.group {
			assertSandboxOwnGroup(t, recPath, p.Pid())
			p.SignalGroup(t, x.signal)
		} else {
			p.Signal(t, x.signal)
		}
		awaitStopLatched()
		if err := os.WriteFile(rig.release, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := p.Wait(t)
	res.WantExit(t, x.wantExit)
	out := res.Stdout + res.Stderr
	if !strings.Contains(out, x.wantOut) {
		t.Errorf("output lacks %q:\n%s", x.wantOut, out)
	}
	assertSandboxSurvivedSettle(t, "output", out)
	primary, _ := filepath.Glob(filepath.Join(hostpaths.LogDir(rig.workDir), "primary-github-*"))
	if x.mode == sandboxTracker && len(primary) == 0 {
		t.Errorf("no primary-github record in %s", hostpaths.LogDir(rig.workDir))
	}
	for _, record := range primary {
		b, _ := os.ReadFile(record)
		assertSandboxSurvivedSettle(t, record, string(b))
	}
	if x.mode == sandboxTracker {
		// The settle's tracker write is a Connector call through the sandbox, so
		// the backend seeing it proves the sandbox still served it; a sandbox
		// stopped first fails the call before it leaves the launcher.
		if got := rig.backend.labelPosts(); !slices.Contains(got, x.wantLabel) {
			t.Errorf("the backend saw labels %q added to #7, want %q among them; requests:\n%s\noutput:\n%s", got, x.wantLabel, rig.backend.requests(), out)
		}
	}

	// The stopped line prints only after a successful stop, so a fail-then-retry
	// still shows one; no failed-attempt warning too is what proves one hook run.
	stopped := stoppedSandboxes(out)
	if failed := strings.Count(out, "warning: stopping Connector sandbox"); len(stopped) != 1 || failed != 0 {
		t.Fatalf("the stop hook ran %d times (%q) with %d failed attempts, want exactly once\noutput:\n%s", len(stopped), stopped, failed, out)
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
	t.Parallel()
	orphan, others := stageOrphan(t)
	for _, imageAbsent := range []bool{false, true} {
		t.Run(fmt.Sprintf("imageAbsent=%t", imageAbsent), func(t *testing.T) {
			t.Parallel()
			rig := newSandboxRig(t, "podman", sandboxRigOpts{
				mode: sandboxLocal, runs: []seamtest.PodmanRun{{Outcome: seamOutcome("ready")}}, issues: []int{7},
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
// owner pid is dead) and the names a reap must leave alone: a sandbox whose
// launcher still runs (this test process), a real Box, names that only resemble
// a sandbox's, and two sandboxes with a dead pid it must not claim: one from an
// older Launcher (no owner) and one from another pid namespace.
func stageOrphan(t *testing.T) (orphan string, others []string) {
	t.Helper()
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	deadPid := dead.Process.Pid
	stamp := time.Unix(0, 1700000000000000000)
	nano := stamp.UnixNano()
	orphan = sandbox.Name(deadPid, stamp)
	_, tail, _ := strings.Cut(strings.TrimPrefix(orphan, runner.ConnectorSandboxPrefix), "-")
	others = []string{
		sandbox.Name(os.Getpid(), stamp),
		fmt.Sprintf("%s%d-%d", runner.ConnectorSandboxPrefix, deadPid, nano),
		runner.ConnectorSandboxPrefix + "feedfacecafe-" + tail,
		dispatch.BoxName("99"),
		"spindrift-box-connector-sandbox-7",
		fmt.Sprintf("box-%s%d-%d", runner.ConnectorSandboxPrefix, deadPid, nano),
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

// sandboxDoc writes a copy of the input document at docPath with row as its
// github backend row, none in sandboxLocal mode. mode picks the row's primary
// key. The fixture row is a routed no-tracker Connector, so its lack of the
// tracker capability refuses the first read. artifacts overrides keys of the
// document's artifacts.
func sandboxDoc(t *testing.T, docPath string, row map[string]any, mode sandboxMode, artifacts map[string]string) string {
	t.Helper()
	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
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
	var rows []any
	if mode != sandboxLocal {
		if mode == sandboxPrimary || mode == sandboxTracker {
			row["primary"] = true
		}
		rows = []any{row}
	}
	if doc["backends"], err = json.Marshal(rows); err != nil {
		t.Fatal(err)
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

// TestSeamConnectorSandboxPrimaryTotals runs a work dispatch under a primary
// github row with the sandbox on, and pins the totals line the launcher appends
// to the run's primary audit file once the sandbox has closed. The fixture
// Connector declares no forge.tokenGrant, so the startup write gate warns and
// continues; it is no tracker either, so the run's first primary read (listing
// issues) is refused as unsupported and the launcher exits 1. The gate's
// token introspection and that read are the two things the .log holds, and the
// audit file holds no Connector decisions, only the totals.
func TestSeamConnectorSandboxPrimaryTotals(t *testing.T) {
	t.Parallel()
	rig := newSandboxRig(t, "podman", sandboxRigOpts{mode: sandboxPrimary, issues: []int{7}})
	res := seamtest.Start(t, rig.cmd).Wait(t)
	res.WantExit(t, 1)
	if starts, _ := countRuns("podman", seamtest.ReadRecord(t, filepath.Join(rig.recDir, "podman.jsonl"))); starts != 1 {
		t.Errorf("the runtime started %d Connector sandboxes, want 1", starts)
	}

	logDir := hostpaths.LogDir(rig.workDir)
	audits, _ := filepath.Glob(filepath.Join(logDir, "primary-github-*.audit.jsonl"))
	if len(audits) != 1 {
		t.Fatalf("primary audit files = %v, want one\nstderr:\n%s", audits, res.Stderr)
	}
	raw, err := os.ReadFile(audits[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("primary audit has %d lines, want just the totals line:\n%s", len(lines), raw)
	}
	var totals map[string]int64
	if err := json.Unmarshal([]byte(lines[0]), &totals); err != nil {
		t.Fatalf("totals line %q: %v", lines[0], err)
	}
	for _, k := range []string{"sandbox_start_ns", "sandbox_calls", "sandbox_hop_ns"} {
		if totals[k] <= 0 {
			t.Errorf("%s = %d, want > 0 (%s)", k, totals[k], lines[0])
		}
	}
	// The call count is deterministic: four from the github fixture, the write
	// gate's token introspection included, and three from claude's Driver
	// Connector, which every run loads in the sandbox now that it is the Driver
	// (issue #6799).
	if totals["sandbox_calls"] != 7 {
		t.Errorf("sandbox_calls = %d, want 7 (%s)", totals["sandbox_calls"], lines[0])
	}
	// A routed read stays inside the sandbox, and the fixture asks the host
	// for nothing.
	if v, ok := totals["sandbox_host_calls"]; !ok || v != 0 {
		t.Errorf("sandbox_host_calls = %d (present %t), want 0 and present (%s)", v, ok, lines[0])
	}
	if len(totals) != 4 {
		t.Errorf("totals line has keys %v, want the four totals", slices.Sorted(maps.Keys(totals)))
	}

	logs, _ := filepath.Glob(filepath.Join(logDir, "primary-github-*.log"))
	if len(logs) != 1 {
		t.Fatalf("primary logs = %v, want one", logs)
	}
	b, _ := os.ReadFile(logs[0])
	logLines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(logLines) != 2 || !strings.Contains(logLines[0], `"op":"introspect the token"`) || !strings.Contains(logLines[1], `"op":"list issues"`) {
		t.Errorf("primary log is not the token introspection then the refused issue listing:\n%s", b)
	}
	if out := res.Stdout + res.Stderr; !strings.Contains(out, "could not be determined") || !strings.Contains(out, "forge.tokenGrant") {
		t.Errorf("the write gate did not warn that the capability is undetermined:\n%s", out)
	}
}

// assertSandboxOwnGroup fails unless every Connector sandbox client the fake
// recorded, which it replaces with the connector-host, leads a process group
// other than the launcher's. A terminal's Ctrl-C reaches every process in the
// launcher's group, so a sandbox left in it dies before the run's settle can
// write through it (issue #7302).
func assertSandboxOwnGroup(t *testing.T, record string, launcher int) {
	t.Helper()
	pids := seamtest.SandboxPids(t, record)
	if len(pids) == 0 {
		t.Fatalf("no Connector sandbox started under launcher pid %d", launcher)
	}
	for _, pid := range pids {
		pgid, err := syscall.Getpgid(pid)
		if err != nil {
			t.Fatalf("sandbox pid %d: %v", pid, err)
		}
		if pgid == launcher {
			t.Errorf("the Connector sandbox (pid %d) shares the launcher's process group %d", pid, launcher)
		}
	}
}
