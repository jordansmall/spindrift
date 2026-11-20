package seamtest

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/fixturebuild"
	"spindrift.dev/launcher/internal/runner"
)

// PodmanConfig is the podman fake's JSON config.
type PodmanConfig struct {
	// Record is the file every invocation's argv is appended to.
	Record string `json:"record"`
	// ImagePresent answers `image exists` / `image inspect`.
	ImagePresent bool `json:"image_present"`
	// Runs scripts the `run` invocations in order; runs past the end behave
	// as the zero PodmanRun (exit 0, no output).
	Runs []PodmanRun `json:"runs"`
	// ConnectorHost is the connector-host binary a Connector sandbox `run`
	// execs in place of an image's entrypoint, so the fake process is the host
	// and the launcher's pipes reach it unchanged. Sandbox runs never consume
	// Runs.
	ConnectorHost string `json:"connector_host"`
	// RefuseSandbox, when set, makes a Connector sandbox `run` exit 125 (the
	// status a container runtime gives a run it could not start) with this
	// message on stderr, scripting a sandbox start failure.
	RefuseSandbox string `json:"refuse_sandbox"`
	// Running names the containers `ps` lists and `inspect` finds, running,
	// with the id "id-<name>": what a hard-killed launcher left behind.
	Running []string `json:"running"`
}

// PodmanRun is one scripted `podman run`.
type PodmanRun struct {
	Exit int `json:"exit"`
	// Outcome is written to stdout, e.g.
	// "SPINDRIFT_OUTCOME issue=7 status=done\n". Like a well-behaved Box it
	// gains the run's own " nonce=<RUN_NONCE>" (from the run argv's
	// -e RUN_NONCE=...) unless it already carries one; the launcher rejects
	// a line without it as spoofed.
	Outcome string `json:"outcome"`
	// Intents are raw issue-intent JSON payloads (a butler finding), each
	// written ahead of Outcome as a "SPINDRIFT_ISSUE_INTENT <RUN_NONCE>
	// <base64>" line, the log-carrier grammar. Without RUN_NONCE in the run
	// argv they are omitted: the launcher would reject them as unverified.
	Intents []string `json:"intents"`
	// Comment is a research verdict comment body, written between the intents
	// and Outcome as a "SPINDRIFT_COMMENT <RUN_NONCE> <base64>" line; a Box with
	// no in-box tracker client relays its verdict that way. Omitted without
	// RUN_NONCE, like Intents.
	Comment string `json:"comment"`
	// Files maps a destination path to a prebuilt source file the run copies
	// there, standing in for what a Box writes into its mounted outbox (a
	// read-only Box's code-out bundle). The launcher empties the outbox before
	// the run, so a test cannot pre-place the file.
	Files map[string]string `json:"files"`
	// HoldUntil is a path the run waits to exist before it writes anything
	// else, so a test can signal the launcher while the Box is in flight.
	HoldUntil string `json:"hold_until"`
}

// podmanMain serves both podman and docker; the invoked name picks the config.
func podmanMain(args []string) int {
	return podmanFake(filepath.Base(os.Args[0]), args, os.Stdout, os.Stderr)
}

// podmanFake mirrors tests/fakes/runtime: every subcommand succeeds silently
// except the probes whose failure the launcher branches on.
func podmanFake(tool string, args []string, stdout, stderr io.Writer) int {
	var cfg PodmanConfig
	if err := loadConfig(tool, &cfg); err != nil {
		fmt.Fprintf(stderr, "%s fake: %v\n", tool, err)
		return fakeConfigExit
	}
	prior, err := appendRecord(cfg.Record, args)
	if err != nil {
		fmt.Fprintf(stderr, "%s fake: record: %v\n", tool, err)
		return fakeConfigExit
	}
	sub := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	switch sub(0) {
	case "image":
		if sub(1) == "inspect" || sub(1) == "exists" {
			if cfg.ImagePresent {
				return 0
			}
			return 1
		}
	case "ps":
		for _, n := range cfg.Running {
			fmt.Fprintln(stdout, n)
		}
		return 0
	case "inspect":
		// Container liveness probe: only a configured Running name exists.
		// The not-found wording is what lets Kill tell this from an
		// unreachable daemon.
		name := args[len(args)-1]
		if slices.Contains(cfg.Running, name) {
			fmt.Fprintf(stdout, "{\"ID\":\"id-%s\",\"State\":{\"Status\":\"running\"}}\n", name)
			return 0
		}
		fmt.Fprintf(stderr, "Error: no such object: %q\n", name)
		return 1
	case "run":
		if isSandboxRun(args) {
			return sandboxRun(tool, cfg, args, stderr)
		}
		n := 0
		for _, p := range prior {
			if len(p) > 0 && p[0] == "run" && !isSandboxRun(p) {
				n++
			}
		}
		var run PodmanRun
		if n < len(cfg.Runs) {
			run = cfg.Runs[n]
		}
		out, err := boxOutput(run, args)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		io.WriteString(stdout, out)
		return run.Exit
	}
	return 0
}

// boxOutput is what a scripted Box run prints: its intents, its comment, then its outcome.
// It also materializes run.Files, the run's other observable effect.
func boxOutput(run PodmanRun, args []string) (string, error) {
	if err := holdUntil(run.HoldUntil); err != nil {
		return "", err
	}
	for dst, src := range run.Files {
		if err := copyFile(src, dst); err != nil {
			return "", fmt.Errorf("box fake: copy %s to %s: %w", src, dst, err)
		}
	}
	var b strings.Builder
	if nonce, ok := runNonce(args); ok {
		for _, raw := range run.Intents {
			fmt.Fprintf(&b, "SPINDRIFT_ISSUE_INTENT %s %s\n", nonce, base64.StdEncoding.EncodeToString([]byte(raw)))
		}
		if run.Comment != "" {
			fmt.Fprintf(&b, "SPINDRIFT_COMMENT %s %s\n", nonce, base64.StdEncoding.EncodeToString([]byte(run.Comment)))
		}
	}
	b.WriteString(withNonce(run.Outcome, args))
	return b.String(), nil
}

// holdUntil blocks until path exists; a test that never releases it fails the
// run instead of hanging it.
func holdUntil(path string) error {
	if path == "" {
		return nil
	}
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
	}
	return fmt.Errorf("box fake: %s never appeared", path)
}

func withNonce(outcome string, args []string) string {
	if outcome == "" || strings.Contains(outcome, " nonce=") {
		return outcome
	}
	nonce, ok := runNonce(args)
	if !ok {
		return outcome
	}
	line := strings.TrimSuffix(outcome, "\n")
	return line + " nonce=" + nonce + outcome[len(line):]
}

// runNonce finds RUN_NONCE in a run argv: `-e RUN_NONCE=<v>` for the OCI
// runtimes, `--setenv RUN_NONCE <v>` for bwrap.
func runNonce(args []string) (string, bool) {
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, "RUN_NONCE="); ok && i > 0 && args[i-1] == "-e" {
			return v, true
		}
		if a == "RUN_NONCE" && i > 0 && args[i-1] == "--setenv" && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// copyFile copies src to dst, creating the parent directory.
func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// isSandboxRun reports whether a `run` argv starts a Connector sandbox: the
// runner names every one with ConnectorSandboxPrefix, which no Box carries.
func isSandboxRun(args []string) bool {
	return slices.ContainsFunc(longFlags(args, "name"), func(n string) bool { return strings.HasPrefix(n, runner.ConnectorSandboxPrefix) })
}

// sandboxRun refuses a sandbox whose argv weakens its isolation, then becomes
// the connector-host. It returns only on a refusal.
func sandboxRun(tool string, cfg PodmanConfig, args []string, stderr io.Writer) int {
	if problems := runner.SandboxViolations(args); len(problems) > 0 {
		fmt.Fprintf(stderr, "%s fake: Connector sandbox isolation: %s\n", tool, strings.Join(problems, "; "))
		return 1
	}
	if cfg.RefuseSandbox != "" {
		fmt.Fprintln(stderr, cfg.RefuseSandbox)
		return 125
	}
	return execConnectorHost(tool, cfg.ConnectorHost, stderr)
}

// execConnectorHost replaces the fake with the connector-host binary at host.
// It returns only when that is impossible.
func execConnectorHost(tool, host string, stderr io.Writer) int {
	if host == "" {
		fmt.Fprintf(stderr, "%s fake: a Connector sandbox run needs connector_host in the config\n", tool)
		return fakeConfigExit
	}
	err := syscall.Exec(host, []string{"connector-host"}, os.Environ())
	fmt.Fprintf(stderr, "%s fake: exec %s: %v\n", tool, host, err)
	return 1
}

// longFlags is every value of the long flag --name, in either `--name=v` or
// `--name v` form.
func longFlags(args []string, name string) []string {
	var vs []string
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, "--"+name+"="); ok {
			vs = append(vs, v)
		} else if a == "--"+name && i+1 < len(args) {
			vs = append(vs, args[i+1])
		}
	}
	return vs
}

// UsePodman makes `podman` on PATH this fake, scripted by cfg, until the test
// ends, and returns the file its argvs are recorded to (cfg.Record, or a fresh
// one). The calling test binary must run seamtest.Main in TestMain. An empty
// cfg.ConnectorHost is filled with the connector-host fixturebuild resolves, so
// a sandbox run reaches the real host.
func UsePodman(t testing.TB, cfg PodmanConfig) (record string) {
	t.Helper()
	if cfg.Record == "" {
		cfg.Record = filepath.Join(t.TempDir(), "podman.jsonl")
	}
	if cfg.ConnectorHost == "" {
		host, err := fixturebuild.ConnectorHost()
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnectorHost = host
	}
	for k, v := range WriteFakeConfig(t, "podman", cfg) {
		t.Setenv(k, v)
	}
	t.Setenv("PATH", InstallFakes(t, "podman")+string(os.PathListSeparator)+os.Getenv("PATH"))
	return cfg.Record
}
