package main

import (
	"fmt"
	"os"
	"testing"

	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectorsetup"
	"spindrift.dev/launcher/internal/connectorsetup/connectorsetuptest"
	"spindrift.dev/launcher/internal/driver/bundle"
	"spindrift.dev/launcher/internal/driver/driverrows"
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
	// The Connector runtime mainRun builds starts its sandbox on the runner
	// family, which a test binary has none for, so the in-process engine
	// comes in through Options.Sandbox; a test of the real wiring passes its own.
	newConnectorRuntime = withClaudeConnector(connectorsetuptest.WithInProcessSandbox(newConnectorRuntime))
	// dispatchrecord still reads the process Set (it parses logs outside any
	// runtime), so claude stays registered there until it takes a Set.
	seamtest.MainWith(m, drivertest.UseClaudeConnector)
}

// withClaudeConnector adds claude's Connector row to the Driver rows the
// runtime under test is built with, as the flake renders it into a real
// document and a test document leaves it out.
func withClaudeConnector(build func(connectorsetup.Input, connectorsetup.Options) (*connectorruntime.Runtime, error)) func(connectorsetup.Input, connectorsetup.Options) (*connectorruntime.Runtime, error) {
	return func(in connectorsetup.Input, opts connectorsetup.Options) (*connectorruntime.Runtime, error) {
		raw, err := drivertest.WithClaudeRow(launcherInput.Drivers)
		if err != nil {
			return nil, err
		}
		if in.Drivers, err = driverrows.Parse(raw, bundle.Drivers()); err != nil {
			return nil, err
		}
		return build(in, opts)
	}
}
