package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
)

// hostEnvironment is the real Environment: host PATH lookups and ambient env
// var reads. LookPath is unused by runQuickstart until host detection lands
// (ADR 0027); wired here so the seam is ready.
type hostEnvironment struct{}

func (hostEnvironment) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func (hostEnvironment) LookupEnv(key string) (string, bool) {
	return os.LookupEnv(key)
}

// hostCommandRunner is the real CommandRunner: runs the named command with
// the process's own stdio. Used for the `claude setup-token` finish-line
// step; `spindrift build` wiring is still unbuilt (ADR 0027).
type hostCommandRunner struct{}

func (hostCommandRunner) Run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func main() {
	force := flag.Bool("force", false, "overwrite an existing flake.nix/harness.env, backing each up to *.bak first")
	flag.Parse()

	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "quickstart: %s\n", err)
		os.Exit(1)
	}

	stat, statErr := os.Stdin.Stat()
	interactive := statErr == nil && (stat.Mode()&os.ModeCharDevice) != 0

	if err := runQuickstart(dir, hostEnvironment{}, hostCommandRunner{}, os.Stdout, os.Stdin, interactive, *force); err != nil {
		fmt.Fprintf(os.Stderr, "quickstart: %s\n", err)
		os.Exit(1)
	}
}
