package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
)

// hostEnvironment is the real Environment: host PATH lookups. Unused by
// runQuickstart until host detection lands (ADR 0027); wired here so the
// seam is ready.
type hostEnvironment struct{}

func (hostEnvironment) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

// hostCommandRunner is the real CommandRunner: runs the named command with
// the process's own stdio. Unused by runQuickstart until the finish-line
// steps (`claude setup-token`, `spindrift build`) land (ADR 0027); wired
// here so the seam is ready.
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
