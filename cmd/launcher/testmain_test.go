package main

import (
	"fmt"
	"os"
	"testing"

	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
	"spindrift.dev/launcher/internal/driver/drivertest"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/seamtest"
)

// TestMain scripts registryProxyTransportFn for the whole package: its
// production default binds a unix listener and execs c.runtime, and doctor
// tests must start no container (issue #3114). Tests of the row itself
// override it with withRegistryProxyTransportFake.
//
// It scripts installStopSignal too (#3520, #3521): the production default
// registers a real signal.Notify(SIGTERM) that stopCleanup deliberately never
// undoes, leaving the test binary deaf to SIGTERM for the rest of the run.
func TestMain(m *testing.M) {
	registryProxyTransportFn = func(config) (registrymanifest.Endpoint, error) {
		return registrymanifest.NewUnixEndpoint(""), nil
	}
	installStopSignal = func() (<-chan struct{}, <-chan struct{}, func()) { return nil, nil, func() {} }
	// loadSchemaConfig reads the host git identity, so a bare call would shell
	// out to git; tests that need a value use fakeHostGitConfig.
	gitConfigLookup = func(string) string { return "" }
	// An ambient GH_HOST (e.g. a GHE dev shell) would otherwise flip every
	// github.com-stubbed origin match, since checkoutIsTargetRepo follows it
	// (issue #3912).
	if err := os.Unsetenv("GH_HOST"); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: os.Unsetenv(GH_HOST): %v\n", err)
		os.Exit(1)
	}
	// A pre-#4291 host launcher still exports its removed knobs into the Box,
	// and removedKnobsCheck would trip on them in every validate().
	for _, k := range removedKnobs {
		if err := os.Unsetenv(k.env); err != nil {
			fmt.Fprintf(os.Stderr, "TestMain: os.Unsetenv(%s): %v\n", k.env, err)
			os.Exit(1)
		}
	}
	// A dev shell or Box exports knob env vars, and mainRun refuses to start
	// with any of them set (ADR 0020).
	for _, e := range inputdoc.SchemaFlags {
		if err := os.Unsetenv(e.Env); err != nil {
			fmt.Fprintf(os.Stderr, "TestMain: os.Unsetenv(%s): %v\n", e.Env, err)
			os.Exit(1)
		}
	}
	// installConnectorSandbox overwrites the Loader UseClaudeConnector
	// installs, so the in-process engine comes in through its builder; tests of
	// the real sandbox wiring put realConnectorSandboxLoader back.
	newConnectorSandboxLoader = func(config, string) connectorsandbox.Loader { return connectorsandboxtest.InProcess() }
	seamtest.MainWith(m, drivertest.UseClaudeConnector)
}
