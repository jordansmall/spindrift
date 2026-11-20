package seamtest

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/fixturebuild"
	"spindrift.dev/launcher/internal/runner"
)

// BwrapConfig is the bwrap fake's JSON config. bwrap has no subcommands, so
// every invocation is a Box run and Runs scripts them in order, except the
// launcher's overlay probe (bwrap.ValidateOverlay), which exits 0 silently
// and takes no scripted run, and a Connector sandbox run, which execs
// ConnectorHost and takes none either.
type BwrapConfig struct {
	// Record is the file every invocation's argv is appended to.
	Record string `json:"record"`
	// Runs scripts the invocations; ones past the end behave as the zero
	// PodmanRun. Outcome gains its nonce from `--setenv RUN_NONCE <v>`.
	Runs []PodmanRun `json:"runs"`
	// ConnectorHost is the connector-host binary a Connector sandbox run
	// execs, so the fake process is the host and the launcher's pipes reach it
	// unchanged.
	ConnectorHost string `json:"connector_host"`
	// RefuseSandbox, when set, makes a Connector sandbox run exit 1 (as bwrap
	// does when it cannot set up its namespaces) with this message on stderr,
	// scripting a sandbox start failure.
	RefuseSandbox string `json:"refuse_sandbox"`
}

// IsBwrapOverlayProbe reports whether argv is the launcher's writable-overlay
// probe (`... --overlay-src <dir> ... -- true`) rather than a Box run.
func IsBwrapOverlayProbe(args []string) bool {
	n := len(args)
	return n >= 2 && args[n-2] == "--" && args[n-1] == "true" && slices.Contains(args, "--overlay-src")
}

// bwrapSandboxHost reports whether argv starts a Connector sandbox, and the
// connector-host store path it runs: the sandbox's program is the last
// argument and ends in /bin/connector-host, which no Box's does.
func bwrapSandboxHost(args []string) (host string, ok bool) {
	if len(args) == 0 {
		return "", false
	}
	return strings.CutSuffix(args[len(args)-1], "/bin/connector-host")
}

func bwrapMain(args []string) int {
	return bwrapFake(args, os.Stdout, os.Stderr)
}

func bwrapFake(args []string, stdout, stderr io.Writer) int {
	var cfg BwrapConfig
	if err := loadConfig("bwrap", &cfg); err != nil {
		fmt.Fprintf(stderr, "bwrap fake: %v\n", err)
		return fakeConfigExit
	}
	prior, err := appendRecord(cfg.Record, args)
	if err != nil {
		fmt.Fprintf(stderr, "bwrap fake: record: %v\n", err)
		return fakeConfigExit
	}
	if IsBwrapOverlayProbe(args) {
		return 0
	}
	if host, ok := bwrapSandboxHost(args); ok {
		return bwrapSandboxRun(cfg, host, args, stderr)
	}
	boxRuns := 0
	for _, p := range prior {
		if _, sandbox := bwrapSandboxHost(p); !IsBwrapOverlayProbe(p) && !sandbox {
			boxRuns++
		}
	}
	var run PodmanRun
	if boxRuns < len(cfg.Runs) {
		run = cfg.Runs[boxRuns]
	}
	out, err := boxOutput(run, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	io.WriteString(stdout, out)
	return run.Exit
}

// bwrapSandboxRun refuses a sandbox whose argv weakens its isolation, then
// becomes the connector-host. It returns only on a refusal.
func bwrapSandboxRun(cfg BwrapConfig, host string, args []string, stderr io.Writer) int {
	if problems := runner.BwrapSandboxViolations(args, host); len(problems) > 0 {
		fmt.Fprintf(stderr, "bwrap fake: Connector sandbox isolation: %s\n", strings.Join(problems, "; "))
		return 1
	}
	if cfg.RefuseSandbox != "" {
		fmt.Fprintln(stderr, cfg.RefuseSandbox)
		return 1
	}
	return execConnectorHost("bwrap", cfg.ConnectorHost, cfg.Record, stderr)
}

// UseBwrap makes `bwrap` on PATH this fake, scripted by cfg, until the test
// ends, and returns the file its argvs are recorded to (cfg.Record, or a fresh
// one). The calling test binary must run seamtest.Main in TestMain. An empty
// cfg.ConnectorHost is filled with the connector-host fixturebuild resolves, so
// a sandbox run reaches the real host.
//
// The launcher starts bwrap with a scrubbed env, so the fake finds its config
// in the working directory: UseBwrap changes into a fresh directory holding it,
// and relative paths in the test no longer resolve.
func UseBwrap(t testing.TB, cfg BwrapConfig) (record string) {
	t.Helper()
	dir := t.TempDir()
	if cfg.Record == "" {
		cfg.Record = filepath.Join(dir, "bwrap.jsonl")
	}
	if cfg.ConnectorHost == "" {
		host, err := fixturebuild.ConnectorHost()
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnectorHost = host
	}
	WriteFakeConfigFile(t, dir, "bwrap", cfg)
	t.Chdir(dir)
	t.Setenv("PATH", InstallFakes(t, "bwrap")+string(os.PathListSeparator)+os.Getenv("PATH"))
	return cfg.Record
}
