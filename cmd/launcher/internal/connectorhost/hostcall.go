package connectorhost

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// hostCalls is the Launcher-side half of a network Connector's Host calls. An
// engine decodes the guest's pointers into bytes, calls these and writes back
// what they return; Route matching, credentials, audit and the response cap
// stay behind it.
type hostCalls interface {
	// httpRequest serves one http_request: raw is the guest's request bytes and
	// bufCap the size of its response buffer. A nil err is the guest's answer,
	// status 0 with a fixed reason in body for a request that did not complete. A
	// non-nil err is fatal and already audited.
	httpRequest(ctx context.Context, raw []byte, bufCap int) (status int, body []byte, header http.Header, err error)
	// refuse audits a Host call the engine refused before serving it (a pointer
	// outside memory, an oversized argument) and returns the fatal error.
	refuse(call, why string) error
}

// networkCalls serves a Network's Host calls. failed records that a fatal
// failure was audited here, so Call does not audit it a second time.
type networkCalls struct {
	n      *Network
	failed bool
}

var _ hostCalls = (*networkCalls)(nil)

func (h *networkCalls) httpRequest(ctx context.Context, raw []byte, bufCap int) (int, []byte, http.Header, error) {
	if len(raw) > maxRequest {
		return 0, nil, nil, h.refuse(hostRequest, fmt.Sprintf("request of %d bytes, cap %d", len(raw), maxRequest))
	}
	status, body, header, err := h.n.do(ctx, raw, min(bufCap, maxHTTPResponse))
	switch {
	case err == nil:
	case errors.Is(err, ErrHTTPSend):
		// A fixed reason: err carries the request URL and whatever Authorize said,
		// which may be the credential. The detail is in the audit entry.
		return 0, []byte(ErrHTTPSend.Error()), nil, nil
	default:
		h.failed = true
		return 0, nil, nil, err
	}
	// Set-Cookie is withheld defensively: nothing a Connector needs rides in
	// one, and a session cookie is the header least meant for a sandboxed guest.
	header.Del("Set-Cookie")
	return status, body, header, nil
}

func (h *networkCalls) refuse(call, why string) error {
	err := fmt.Errorf("%w: connector %q: %s.%s: %q", ErrMalformedOutput, h.n.Name, hostModule, call, why)
	// No request completed, so this is the call's one audit entry.
	h.n.recordFailure(DecisionRefused, err)
	h.failed = true
	return err
}
