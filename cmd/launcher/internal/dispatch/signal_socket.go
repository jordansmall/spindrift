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
	"spindrift.dev/launcher/internal/signalsocket"
	"spindrift.dev/launcher/internal/signalwire"
)

// signalSocketEndpointEnv and signalSocketSecretEnv name the two env vars
// driver-exec/signal_cmd.go reads in-Box (signalEndpointEnv/signalSecretEnv
// there). The two packages cannot share the constant -- driver-exec is a
// separate binary that must not link this package's dependencies -- so the
// literal is pinned on both sides instead.
const (
	signalSocketEndpointEnv = "SIGNAL_SOCKET_ENDPOINT"
	signalSocketSecretEnv   = "SIGNAL_SOCKET_SECRET"
)

// tcpFallbackBlockedReason is the clause the Signal-socket (here) and
// registry-proxy (box.go) gates share when they reject a TCP verdict under a
// NETWORK_MODE that denies host loopback. A const so go vet still checks the
// composed formats. One %s: the target's name, passed after the caller's own
// NETWORK_MODE=%s argument.
const tcpFallbackBlockedReason = "this runtime can only reach the %s over its TCP fallback, which this mode blocks"

// startSignalSocket starts the Signal socket for this Dispatch under
// BOX_SIGNAL_CARRIER=socket (ADR 0052, issue #3725), given the transport
// verdict runOnce's one probe already took. It mirrors the registry proxy's
// own per-verdict shape (box.go): a unix verdict returns a socket mount to
// append to boxSockets, a TCP verdict returns a runner.SignalSocketLocation
// and forwards the secret through env, and either way it sets d.signalBuffer
// so outcomeResult can read it once the Box exits. The returned close func
// must be deferred by the caller exactly like the registry proxy's; it stops
// the listener but never touches the buffer, which stays readable after Close
// (settle reads it after the Box has already exited).
func (d *Dispatch) startSignalSocket(transport registrymanifest.Endpoint, tcpAddHost bool, env map[string]string, logw io.Writer) (*runner.SocketMount, runner.SignalSocketLocation, func() error, error) {
	// A TCP verdict needs loopback reachability the way the registry proxy's
	// TCP fallback does; NETWORK_MODE=none is already refused earlier at
	// launcher startup (checkSignalCarrierNetworkModeGate). A live probe under
	// no-host-loopback already errors in runOnce (box.go) before reaching
	// here, and the probe cache refuses to replay one (#3775); this gate
	// is a backstop. Either way it fails capability-style, never falling
	// back to the log carrier silently.
	if transport.IsTCP() && runner.DeniesHostLoopback(d.cfg.NetworkMode) {
		return nil, runner.SignalSocketLocation{}, nil, fmt.Errorf("signal socket: BOX_SIGNAL_CARRIER=socket is unsupported under NETWORK_MODE=%s -- "+tcpFallbackBlockedReason+"; use BOX_SIGNAL_CARRIER=log or a different NETWORK_MODE", d.cfg.NetworkMode, "Signal socket")
	}

	buf := signalsocket.New(d.signalSocketConfig())

	switch {
	case transport.IsUnix():
		dir, err := signalSocketDir()
		if err != nil {
			return nil, runner.SignalSocketLocation{}, nil, fmt.Errorf("signal socket: %w", err)
		}

		socketPath := filepath.Join(dir, signalSocketFile)
		l := &signalsocket.Listener{Handler: signalsocket.NewHandler(buf, logw)}
		if err := l.ListenAndServe(socketPath); err != nil {
			os.RemoveAll(dir) //nolint:errcheck
			return nil, runner.SignalSocketLocation{}, nil, fmt.Errorf("signal socket: %w", err)
		}

		// The in-Box mount TARGET, never the host source: a Box-side reader
		// can only dial the path it sees inside its own mount namespace, the
		// same reason box.go's registry-proxy manifestEndpoint names
		// RegistryProxySocketTarget rather than the minted host path.
		env[signalSocketEndpointEnv] = "unix://" + runner.SignalSocketTarget
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
		handler, err := signalsocket.NewGatedHandler(buf, logw, secret)
		if err != nil {
			return nil, runner.SignalSocketLocation{}, nil, fmt.Errorf("signal socket: %w", err)
		}

		l := &signalsocket.Listener{Handler: handler}
		// The listener binds every interface, not loopback, mirroring the
		// registry proxy's own TCP bind (box.go, issue #3111 review
		// finding): the Box reaches it only via --add-host
		// <host>:host-gateway, which on a plain Linux docker bridge
		// resolves to the bridge IP, so a loopback-only bind would leave
		// nothing on the address the Box dials.
		if err := l.ListenAndServeTCP("0.0.0.0:0"); err != nil {
			return nil, runner.SignalSocketLocation{}, nil, fmt.Errorf("signal socket: %w", err)
		}
		tcpAddr, ok := l.Addr().(*net.TCPAddr)
		if !ok {
			// The unix branch above unwinds its own half-built state on
			// every error; this branch owes the same, or a bound port
			// outlives the Dispatch that opened it.
			l.Close() //nolint:errcheck
			return nil, runner.SignalSocketLocation{}, nil, fmt.Errorf("signal socket: TCP listener address %v is not a *net.TCPAddr", l.Addr())
		}

		env[signalSocketEndpointEnv] = fmt.Sprintf("http://%s:%d", transport.Host(), tcpAddr.Port)
		// SIGNAL_SOCKET_SECRET stays its own env var, exactly like
		// REGISTRY_PROXY_TCP_SECRET (box.go): it is a bearer-token-shaped
		// credential, so bwrap.go's offArgvKeys keeps it off argv.
		env[signalSocketSecretEnv] = secret
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

// signalSocketConfig derives this Dispatch's signalsocket.Config: Consumes
// always lists all three kinds, never a narrower per-Dispatch set derived
// from the forge/tracker capability bits, since the log carrier drops a
// signal the run never consumes silently, and this issue's contract is that
// both carriers produce the same Result, so a narrower Consumes here would
// make the carriers differ on which signals a given run accepts.
// Deliberately deferred, not overlooked. Only a Chore run overrides the
// socket's default issue-intent cap, with its own findings room.
func (d *Dispatch) signalSocketConfig() signalsocket.Config {
	cfg := signalsocket.Config{
		Consumes: []signalwire.Kind{signalwire.KindComment, signalwire.KindPRIntent, signalwire.KindIssueIntent},
	}
	if d.subject.key.IsChore() {
		cfg.MaxIssueIntents = d.subject.chore.maxIssueIntents()
	}
	return cfg
}
