package connectorhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync/atomic"
	"time"
)

// closeGrace is how long close waits for the host to exit once its stdin is
// closed before it kills it.
const closeGrace = 5 * time.Second

// LoadRemote is Load over an out-of-process connector host: it starts cmd, a
// process running Serve on its stdin and stdout, sends it wasm and checks the
// handshake. The module runs under the limits Load applies, in the host.
// cmd.Stderr is the caller's; the host logs there and nothing else. The Connector
// owns cmd from here: Close ends it, and a failed load kills it.
func LoadRemote(ctx context.Context, wasm []byte, cmd *exec.Cmd) (*Connector, error) {
	return loadWith(ctx, wasm, processEngine(cmd), nil)
}

// LoadNetworkRemote is LoadNetwork over an out-of-process connector host. The
// host is granted the Host call but never serves it: each http_request crosses
// back to the Launcher, which applies n's Routes, attaches the credential and
// audits, so the host never holds a credential.
func LoadNetworkRemote(ctx context.Context, wasm []byte, n Network, cmd *exec.Cmd) (*Connector, error) {
	if err := checkGrant(n); err != nil {
		return nil, err
	}
	return loadWith(ctx, wasm, processEngine(cmd), &n)
}

// hostLink is the Launcher's end of a connector host: the stream it sends
// frames on and reads them from, and how to end the host.
type hostLink struct {
	r io.Reader
	w io.WriteCloser
	// kill stops the host without waiting; safe concurrently and after wait.
	kill func()
	// wait blocks until the host has exited, once w is closed or kill ran.
	wait func() error
}

// processEngine starts cmd and runs a remote engine over its stdin and stdout.
func processEngine(cmd *exec.Cmd) engineFactory {
	return func(ctx context.Context, wasm []byte, host hostCalls) (engine, error) {
		in, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			_ = in.Close()
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return newRemoteEngine(ctx, wasm, host, hostLink{
			r: out, w: in, wait: cmd.Wait,
			kill: func() { _ = cmd.Process.Kill() },
		})
	}
}

// remoteEngine is the engine whose module runs in a connector host. The call
// path alone writes frames and abort only kills the host, so no lock guards the
// stream beyond the Connector's mu.
type remoteEngine struct {
	link hostLink
	// host serves the Host calls the host sends back; nil when the grant is none.
	host hostCalls
	// killed records that this side ended the host, so its exit status is not a
	// failure of close.
	killed atomic.Bool
	closed bool
}

var _ engine = (*remoteEngine)(nil)

// newRemoteEngine sends wasm to the host and waits for it to load, and ends the
// host if it does not.
func newRemoteEngine(ctx context.Context, wasm []byte, host hostCalls, l hostLink) (*remoteEngine, error) {
	e := &remoteEngine{link: l, host: host}
	if err := e.load(ctx, wasm); err != nil {
		e.abort()
		_ = e.close(context.WithoutCancel(ctx))
		return nil, err
	}
	return e, nil
}

func (e *remoteEngine) load(ctx context.Context, wasm []byte) error {
	// Checked here so an over-cap module is a plain error naming the size, as
	// invalid wasm is in-process, not a host that appears to have exited.
	if len(wasm)+loadOverhead > maxFrame {
		return fmt.Errorf("load: module of %d bytes does not fit the %d byte frame cap", len(wasm), maxFrame)
	}
	grant := byte(0)
	if e.host != nil {
		grant = 1
	}
	out, err := e.exchange(ctx, "load", initLimits, kindLoad, []byte{grant}, wasm)
	if err != nil {
		return err
	}
	if len(out) != 0 {
		return fmt.Errorf("load: %w: result of %d bytes, want none", ErrMalformedOutput, len(out))
	}
	return nil
}

func (e *remoteEngine) handshake(ctx context.Context) ([]byte, error) {
	return e.output(ctx, "connector_handshake", limitsFor(0), kindHandshake)
}

func (e *remoteEngine) call(ctx context.Context, req []byte) ([]byte, error) {
	return e.output(ctx, "connector_call", limitsFor(len(req)), kindCall, req)
}

// output is an exchange whose result is a JSON document within lim's cap.
func (e *remoteEngine) output(ctx context.Context, name string, lim limits, kind byte, parts ...[]byte) ([]byte, error) {
	out, err := e.exchange(ctx, name, lim, kind, parts...)
	if err != nil {
		return nil, err
	}
	if err := checkOutputLen(name, uint64(len(out)), lim.output); err != nil {
		return nil, err
	}
	if err := checkOutputJSON(name, out); err != nil {
		return nil, err
	}
	return out, nil
}

// exchange sends one load, handshake or call and serves the host's frames until
// it answers. The budget runs here as well as in the host, because a host that
// hangs or a Host call that stalls cannot be trusted to enforce it.
func (e *remoteEngine) exchange(ctx context.Context, name string, lim limits, kind byte, parts ...[]byte) ([]byte, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, lim.budget, ErrBudget)
	defer cancel()
	// A frame read cannot be interrupted, so the host is killed when ctx ends.
	stop := context.AfterFunc(ctx, e.abort)
	defer stop()
	if err := writeFrame(e.link.w, kind, parts...); err != nil {
		return nil, e.broken(ctx, name, err)
	}
	// The error a Host call failed closed with, which the host's failure frame
	// stands for.
	var hostErr error
	for {
		got, payload, err := readFrame(e.link.r)
		if err != nil {
			return nil, e.broken(ctx, name, err)
		}
		if hostErr != nil && got != kindFailure {
			// The audit already says the call failed closed, so the host may only
			// unwind: a result or another call would turn it into a success.
			err = fmt.Errorf("%w: frame kind %d after a failed Host call", ErrMalformedOutput, got)
			return nil, e.broken(ctx, name, err)
		}
		switch got {
		case kindResult:
			// A result that races the budget is not a success: the AfterFunc has
			// begun killing the host, so the next call would fail as a trap.
			if !stop() {
				return nil, e.broken(ctx, name, errors.New("result arrived as the call's context ended"))
			}
			return payload, nil
		case kindFailure:
			err := decodeFailure(payload)
			if !errors.Is(err, errRemoteHostCall) {
				// A call fails with a named sentinel whatever the engine, so a
				// failure with code 0 is a host breaking the protocol; a load alone
				// fails unnamed, on invalid wasm.
				if re := (*remoteError)(nil); kind != kindLoad && errors.As(err, &re) && re.sentinel == nil {
					err = fmt.Errorf("%w: unnamed failure: %w", ErrMalformedOutput, err)
				}
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			if hostErr == nil {
				return nil, fmt.Errorf("%s: %w: host reported a failed Host call that never crossed", name, ErrMalformedOutput)
			}
			return nil, fmt.Errorf("%s: %w", name, hostErr)
		case kindHTTPRequest:
			err = e.serveHTTPRequest(ctx, payload, &hostErr)
		case kindRefuse:
			err = e.serveRefuse(payload, &hostErr)
		default:
			err = fmt.Errorf("%w: unexpected frame kind %d", ErrMalformedOutput, got)
		}
		if err != nil {
			return nil, e.broken(ctx, name, err)
		}
	}
}

// serveHTTPRequest answers one http_request frame through the Host-call
// handler. A fatal handler error is kept in hostErr and the host told to
// unwind; err is only a stream or protocol failure.
func (e *remoteEngine) serveHTTPRequest(ctx context.Context, payload []byte, hostErr *error) error {
	if e.host == nil {
		return fmt.Errorf("%w: http_request without a grant", ErrMalformedOutput)
	}
	bufCap, raw, err := decodeHTTPRequest(payload)
	if err != nil {
		return err
	}
	status, body, header, herr := e.host.httpRequest(ctx, raw, bufCap)
	if herr != nil {
		*hostErr = herr
		return writeFrame(e.link.w, kindReplyFatal)
	}
	return writeFrame(e.link.w, kindReply, replyParts(status, body, header)...)
}

// serveRefuse records the audit and fatal error for a Host call the host
// refused before it crossed.
func (e *remoteEngine) serveRefuse(payload []byte, hostErr *error) error {
	if e.host == nil {
		return fmt.Errorf("%w: refusal without a grant", ErrMalformedOutput)
	}
	call, why, err := decodeRefuse(payload)
	if err != nil {
		return err
	}
	// call and why reach the audit entry, and the engine refuses only this call.
	if call != hostRequest {
		return fmt.Errorf("%w: refusal of unknown Host call %q", ErrMalformedOutput, call)
	}
	*hostErr = e.host.refuse(call, why)
	return nil
}

// broken names why an exchange with the host stopped: the time budget, the
// caller's context, a frame the Launcher cannot read, or the host's exit.
func (e *remoteEngine) broken(ctx context.Context, name string, err error) error {
	switch {
	case errors.Is(context.Cause(ctx), ErrBudget):
		return fmt.Errorf("%s: %w: %w", name, ErrBudget, err)
	case ctx.Err() != nil:
		return fmt.Errorf("%s: %w: %w", name, ctx.Err(), err)
	case errors.Is(err, ErrMalformedOutput):
		return fmt.Errorf("%s: %w", name, err)
	}
	return fmt.Errorf("%s: %w: connector host exited: %w", name, ErrTrap, err)
}

func (e *remoteEngine) abort() {
	e.killed.Store(true)
	e.link.kill()
}

// close ends the host: its stdin closes, which it takes as the end of the
// session, and it is killed if it has not exited within closeGrace or ctx ends.
func (e *remoteEngine) close(ctx context.Context) error {
	if e.closed {
		return nil
	}
	e.closed = true
	_ = e.link.w.Close()
	// wait blocks until the host exits; either timer kills it, which ends wait.
	timer := time.AfterFunc(closeGrace, e.abort)
	defer timer.Stop()
	stop := context.AfterFunc(ctx, e.abort)
	defer stop()
	err := e.link.wait()
	if e.killed.Load() {
		return nil
	}
	return err
}
