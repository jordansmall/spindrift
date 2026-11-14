package seamtest

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"syscall"
)

// NixConfig is the nix fake's JSON config. It models only the
// `nix develop .#<attr> ...` probe the Box's toolchain decision runs.
type NixConfig struct {
	// Record is the file every invocation's argv is appended to.
	Record string `json:"record"`
	// DevShells are the flake attrs whose `develop` succeeds; any other attr
	// exits 1, as a flake without that devShell does.
	DevShells []string `json:"devShells"`
	// Exec, when set, makes `nix run <ref> -- <verb> ...` exec this program in
	// place of building the app: the fake stands in for the wrapper `nix run`
	// resolves, so a daemon's child is the real launcher. ExecArgs are the
	// flags that wrapper bakes in, placed before the verb. `doctor`, the
	// daemon's startup preflight, just succeeds unless ExecDoctor is set.
	Exec     string   `json:"exec"`
	ExecArgs []string `json:"exec_args"`
	// ExecDoctor execs `doctor` like any other verb, for a test that needs
	// the real preflight to run.
	ExecDoctor bool `json:"exec_doctor"`
}

func nixMain(args []string) int {
	return nixFake(args, os.Stderr)
}

func nixFake(args []string, stderr io.Writer) int {
	var cfg NixConfig
	if err := loadConfig("nix", &cfg); err != nil {
		fmt.Fprintf(stderr, "nix fake: %v\n", err)
		return fakeConfigExit
	}
	if _, err := appendRecord(cfg.Record, args); err != nil {
		fmt.Fprintf(stderr, "nix fake: record: %v\n", err)
		return fakeConfigExit
	}
	if nixRunStubsDoctor(cfg, args) {
		return 0
	}
	if prog, argv, ok := nixRunExec(cfg, args); ok {
		err := syscall.Exec(prog, argv, os.Environ())
		fmt.Fprintf(stderr, "nix fake: exec %s: %v\n", prog, err)
		return fakeConfigExit
	}
	if len(args) >= 2 && args[0] == "develop" && strings.HasPrefix(args[1], ".#") {
		for _, name := range cfg.DevShells {
			if args[1] == ".#"+name {
				return 0
			}
		}
	}
	fmt.Fprintf(stderr, "nix fake: no such devShell: %v\n", args)
	return 1
}

// nixRunRest is the app arguments of a configured `nix run <ref> -- <args>`,
// nil for any other invocation.
func nixRunRest(cfg NixConfig, args []string) []string {
	if cfg.Exec == "" || len(args) == 0 || args[0] != "run" {
		return nil
	}
	i := slices.Index(args, "--")
	if i < 0 {
		return nil
	}
	return args[i+1:]
}

// nixRunStubsDoctor reports whether args is a configured `nix run` of
// `doctor` that the fake answers with success instead of exec'ing it.
func nixRunStubsDoctor(cfg NixConfig, args []string) bool {
	rest := nixRunRest(cfg, args)
	return !cfg.ExecDoctor && len(rest) > 0 && rest[0] == "doctor"
}

// nixRunExec is the program and argv a configured `nix run` execs.
func nixRunExec(cfg NixConfig, args []string) (prog string, argv []string, ok bool) {
	rest := nixRunRest(cfg, args)
	if len(rest) == 0 {
		return "", nil, false
	}
	argv = append(append([]string{cfg.Exec}, cfg.ExecArgs...), rest...)
	return cfg.Exec, argv, true
}
