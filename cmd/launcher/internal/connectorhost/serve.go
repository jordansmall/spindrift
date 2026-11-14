package connectorhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// Serve is the out-of-process connector host: it reads frames from r and
// answers on w until r ends between frames. The first frame loads the module,
// which Serve runs in the interpreter under the limits Load applies; a Connector
// granted Host calls has each http_request cross back to the Launcher, which
// holds the credential and the audit log, so Serve never sees either.
//
// A module failure is reported on w. Serve then returns nil, except that a
// missing export leaves the module usable and the loop running; the Launcher
// closes the host after any other failure. A broken stream or a frame out of
// turn is an error.
func Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	kind, payload, err := readFrame(r)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	if kind != kindLoad {
		return fmt.Errorf("first frame is kind %d, want a load", kind)
	}
	if len(payload) < 1 {
		return errors.New("load frame is missing its grant byte")
	}
	var host hostCalls
	if payload[0] != 0 {
		host = &pipeCalls{r: r, w: w}
	}
	e, err := interpretedEngine()(ctx, payload[1:], host)
	if err != nil {
		return writeFrame(w, kindFailure, failureParts(err)...)
	}
	defer func() { _ = e.close(context.WithoutCancel(ctx)) }()
	if err := writeFrame(w, kindResult); err != nil {
		return err
	}
	for {
		kind, payload, err := readFrame(r)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var out []byte
		switch kind {
		case kindHandshake:
			out, err = e.handshake(ctx)
		case kindCall:
			out, err = e.call(ctx, payload)
		default:
			return fmt.Errorf("unexpected frame kind %d", kind)
		}
		if err == nil {
			if err := writeFrame(w, kindResult, out); err != nil {
				return err
			}
			continue
		}
		if werr := writeFrame(w, kindFailure, failureParts(err)...); werr != nil {
			return werr
		}
		// A missing export leaves the module usable; any other failure leaves it
		// in an unknown state, which the Launcher closes.
		if !errors.Is(err, ErrExport) {
			return nil
		}
	}
}

// pipeCalls is the host's hostCalls: each Host call is sent to the Launcher,
// which answers on the same stream. It runs on the goroutine inside a call, so
// it owns the stream until the call returns.
type pipeCalls struct {
	r io.Reader
	w io.Writer
}

var _ hostCalls = (*pipeCalls)(nil)

func (p *pipeCalls) httpRequest(_ context.Context, raw []byte, bufCap int) (int, []byte, http.Header, error) {
	if err := writeFrame(p.w, kindHTTPRequest, u32(bufCap), raw); err != nil {
		return 0, nil, nil, err
	}
	kind, payload, err := readFrame(p.r)
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		return 0, nil, nil, err
	}
	switch kind {
	case kindReply:
		return decodeReply(payload)
	case kindReplyFatal:
		return 0, nil, nil, errRemoteHostCall
	}
	return 0, nil, nil, fmt.Errorf("unexpected frame kind %d answering http_request", kind)
}

// refuse hands the Launcher a Host call the engine refused; it audits the
// refusal and the host needs no answer.
func (p *pipeCalls) refuse(call, why string) error {
	if err := writeFrame(p.w, kindRefuse, u32(len(call)), []byte(call), []byte(why)); err != nil {
		return err
	}
	return errRemoteHostCall
}
