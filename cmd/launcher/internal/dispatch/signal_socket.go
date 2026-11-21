package dispatch

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/signalclient"
	"spindrift.dev/launcher/internal/signalsocket"
	"spindrift.dev/launcher/internal/signalwire"
)

// tcpFallbackBlockedReason is the clause the Signal-socket and registry-proxy
// gates share when they reject a TCP verdict under a NETWORK_MODE that denies
// host loopback. A const so go vet still checks the composed formats. Its one
// %s, the target's name, comes after the caller's own NETWORK_MODE=%s argument.
const tcpFallbackBlockedReason = "this runtime can only reach the %s over its TCP fallback, which this mode blocks"

// startSignalSocket starts the Signal socket for this Dispatch under
// BOX_SIGNAL_CARRIER=socket (ADR 0052, issue #3725), given runOnce's transport
// verdict: a unix verdict returns a socket mount, a TCP verdict a
// runner.SignalSocketLocation plus the secret in env. Either way it sets
// d.signalBuffer. The caller defers the returned close func, which stops the
// listener but leaves the buffer readable (settle reads it after the Box has
// exited).
func (d *Dispatch) startSignalSocket(transport registrymanifest.Endpoint, tcpAddHost bool, env map[string]string, logw io.Writer) (*runner.SocketMount, runner.SignalSocketLocation, func() error, error) {
	// Backstop: a live probe under no-host-loopback already errors in runOnce,
	// and the probe cache refuses to replay one (#3775). It fails
	// capability-style, never falling back to the log carrier silently.
	if transport.IsTCP() && d.cfg.NetworkMode.DeniesHostLoopback() {
		return nil, runner.SignalSocketLocation{}, nil, fmt.Errorf("signal socket: BOX_SIGNAL_CARRIER=socket is unsupported under NETWORK_MODE=%s -- "+tcpFallbackBlockedReason+"; use --box-signal-carrier log or a different NETWORK_MODE", d.cfg.NetworkMode, "Signal socket")
	}

	buf := signalsocket.New(d.signalSocketConfig())

	switch {
	case transport.IsUnix():
		dir, err := signalSocketDir()
		if err != nil {
			return nil, runner.SignalSocketLocation{}, nil, fmt.Errorf("signal socket: %w", err)
		}

		socketPath := filepath.Join(dir, signalSocketFile)
		l := &signalsocket.Listener{Handler: signalsocket.NewHandler(buf, logw, d.cfg.IssueReader, d.cfg.IssueSearcher)}
		if err := l.ListenAndServe(socketPath); err != nil {
			os.RemoveAll(dir) //nolint:errcheck
			return nil, runner.SignalSocketLocation{}, nil, fmt.Errorf("signal socket: %w", err)
		}

		// The in-Box mount TARGET, never the host source: a Box-side reader
		// can only dial the path it sees inside its own mount namespace.
		env[signalclient.EndpointEnv] = "unix://" + runner.SignalSocketTarget
		d.signalBuffer = buf

		mount := runner.SocketMount{Source: socketPath, Target: runner.SignalSocketTarget}
		closeFn := func() error {
			err := l.Close()
			if rmErr := os.RemoveAll(dir); err == nil {
				err = rmErr
			}
			return err
		}
		return &mount, runner.SignalSocketLocation{}, closeFn, nil

	case transport.IsTCP():
		secret, err := signalsocket.NewSecret()
		if err != nil {
			return nil, runner.SignalSocketLocation{}, nil, fmt.Errorf("signal socket: %w", err)
		}
		handler, err := signalsocket.NewGatedHandler(buf, logw, secret, d.cfg.IssueReader, d.cfg.IssueSearcher)
		if err != nil {
			return nil, runner.SignalSocketLocation{}, nil, fmt.Errorf("signal socket: %w", err)
		}

		l := &signalsocket.Listener{Handler: handler}
		// Binds every interface for the same reason as the registry proxy's
		// TCP bind (issues #3111, #3772): signalwire.SecretHeader is this
		// listener's sole gate.
		if err := l.ListenAndServeTCP("0.0.0.0:0"); err != nil {
			return nil, runner.SignalSocketLocation{}, nil, fmt.Errorf("signal socket: %w", err)
		}
		tcpAddr, ok := l.Addr().(*net.TCPAddr)
		if !ok {
			// Close it, or a bound port outlives the Dispatch that opened it.
			l.Close() //nolint:errcheck
			return nil, runner.SignalSocketLocation{}, nil, fmt.Errorf("signal socket: TCP listener address %v is not a *net.TCPAddr", l.Addr())
		}

		env[signalclient.EndpointEnv] = fmt.Sprintf("http://%s:%d", transport.Host(), tcpAddr.Port)
		// A bearer credential, which offargv.go's OffArgvKeys keeps off argv.
		env[signalclient.SecretEnv] = secret
		d.signalBuffer = buf

		loc := runner.SignalSocketLocation{
			Endpoint:   registrymanifest.NewTCPEndpoint(transport.Host(), strconv.Itoa(tcpAddr.Port)),
			TCPAddHost: tcpAddHost,
		}
		return nil, loc, l.Close, nil

	default:
		return nil, runner.SignalSocketLocation{}, nil, fmt.Errorf("signal socket: transport probe returned neither a unix nor a tcp endpoint")
	}
}

// signalSocketConfig derives this Dispatch's signalsocket.Config. Consumes
// always lists all three kinds, never a narrower capability-derived set: both
// carriers must produce the same Result, so a narrower set would make them
// differ on which signals a run accepts. Only a Chore run overrides the
// default issue-intent cap.
func (d *Dispatch) signalSocketConfig() signalsocket.Config {
	cfg := signalsocket.Config{
		Consumes: []signalwire.Kind{signalwire.KindComment, signalwire.KindPRIntent, signalwire.KindIssueIntent},
	}
	if d.subject.key.IsChore() {
		cfg.MaxIssueIntents = d.subject.chore.maxIssueIntents()
	}
	return cfg
}
