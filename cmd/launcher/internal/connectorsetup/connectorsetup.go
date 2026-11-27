// Package connectorsetup builds the Connector runtime a Launcher process
// hosts Connectors in. The Launcher and the Daemon both build it here, so the
// credential resolution, the sandbox, the credential refresh and the closing
// order are one implementation (#7554).
package connectorsetup

import (
	"context"
	"errors"
	"time"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/sandbox"
	"spindrift.dev/launcher/internal/driver/driverrows"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/rowcredential"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/secretcmd"
)

// Input is what the Launcher input says about hosting Connectors.
type Input struct {
	// Dir is the working directory the sandbox's runner starts from.
	Dir        string
	RunnerKind string
	Runtime    string
	Artifacts  inputdoc.Artifacts
	Knobs      runner.KnobLookup
	// Forge and Tracker are this run's CODE_FORGE and ISSUE_TRACKER; a row
	// neither names is not read, so "" reads none.
	Forge, Tracker string
	// SecretCmd is the SECRET_CMD template, "" for none. The Launcher passes
	// globalSecretCmdTemplate (--secret-cmd, then SECRET_CMD); the Daemon the
	// process's SECRET_CMD.
	SecretCmd string
	// Setting is a credential env's plain value and Applied reports an env the
	// caller already applied this run (see rowcredential.Resolution). The
	// Launcher passes backendRowSetting (flag, document, then environment);
	// the Daemon childEnvValue (document, then environment).
	Setting func(env string) string
	Applied func(env string) bool
	// Drivers are the parsed Driver rows (driverrows.Parse) the runtime offers
	// through Drivers; the zero value offers none.
	Drivers driverrows.Rows
}

// Options is how the Launcher and the Daemon differ: only the Daemon sets
// RefreshEvery and OnRefreshError, and only the Launcher tolerates an unset
// credential.
type Options struct {
	// Interactive lets a credential's `_CMD` prompt on the terminal.
	Interactive bool
	// RequireCredential refuses a row this run reads whose credential is
	// unset. The Launcher tolerates it, since doctor must run without one.
	RequireCredential bool
	// RefreshEvery re-runs the `_CMD` credential sources this often until the
	// runtime closes; 0 never refreshes. It needs OnRefreshError.
	RefreshEvery time.Duration
	// OnRefreshError reports a refresh that failed, the previous value kept.
	// Required when RefreshEvery is set, so each program words its own report.
	OnRefreshError func(env string, err error)
	// Sandbox is where Connectors load; nil starts them through the runner
	// family, on the first load. A test passes the in-process engine.
	Sandbox connectorsandbox.Loader
	// Run runs a credential's `_CMD`; nil is secretcmd.Run. A test seam.
	Run func(cmd string) (string, error)
}

// New resolves every row's credential, builds the Connector sandbox and
// returns the runtime over the registered rows and in.Drivers. Closing the
// runtime, once its sessions close, signals the refresh to stop, then closes the
// sandbox, then records each backend's sandbox totals. It builds nothing on an
// error.
func New(in Input, opts Options) (*connectorruntime.Runtime, error) {
	if opts.RefreshEvery > 0 && opts.OnRefreshError == nil {
		return nil, errors.New("connectorsetup: RefreshEvery needs OnRefreshError")
	}
	run := opts.Run
	if run == nil {
		run = func(cmd string) (string, error) { return secretcmd.Run(cmd, opts.Interactive) }
	}
	sourced, err := rowcredential.Resolution{
		Forge: in.Forge, Tracker: in.Tracker, Template: in.SecretCmd,
		Run: run, Applied: in.Applied, Lookup: in.Setting,
	}.Sources(backendrows.All())
	if err != nil && (opts.RequireCredential || !errors.Is(err, rowcredential.ErrUnset)) {
		return nil, err
	}

	l := opts.Sandbox
	if l == nil {
		l = sandbox.ForKind(in.RunnerKind, runner.Config{Runtime: in.Runtime}, in.Artifacts, in.Knobs, in.Dir)
	}
	rt := connectorruntime.FromRegistries(sourced.Rows, l)
	rt.UseDriverRows(in.Drivers)

	if opts.RefreshEvery > 0 && len(sourced.Commands) > 0 {
		stop := make(chan struct{})
		go func() {
			t := time.NewTicker(opts.RefreshEvery)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					select {
					case <-stop:
						return
					default:
					}
					sourced.Refresh(opts.OnRefreshError)
				}
			}
		}()
		// Never wait for the goroutine: a `_CMD` hung at shutdown (an `op read`
		// awaiting approval) must not block the sandbox close behind it. A tick
		// can win the select against stop, so it re-checks stop: at most the one
		// refresh already past that check when Close runs can still run after it.
		rt.OnClose(func() error { close(stop); return nil })
	}
	rt.OnClose(func() error {
		var err error
		if cl, ok := l.(interface{ Close(context.Context) error }); ok {
			err = cl.Close(context.Background())
		}
		if st, ok := l.Stats(); ok {
			for _, s := range backendkit.Strategies() {
				if s.RecordSandboxTotals != nil {
					s.RecordSandboxTotals(rt, st)
				}
			}
		}
		return err
	})
	return rt, nil
}
