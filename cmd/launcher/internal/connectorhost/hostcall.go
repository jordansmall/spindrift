package connectorhost

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/tetratelabs/wazero/api"
)

// hostModule, hostRequest and hostResponseHeader name the Host calls a network
// Connector gets.
const (
	hostModule         = "spindrift"
	hostRequest        = "http_request"
	hostResponseHeader = "http_response_header"

	// maxHeaderName bounds the name a guest looks up; no real header is longer.
	maxHeaderName = 256
)

// instantiateNetwork links spindrift.http_request and
// spindrift.http_response_header, so a module importing either links; Load
// never calls them, which keeps a Driver granted no Host calls.
//
// The ABI is http_request(reqPtr, reqLen, respPtr, respCap i32) -> i64. The
// guest points at a JSON httpRequest and at a buffer for the answer; the result
// is status<<32 | n, with the n body bytes at respPtr. Status 0 means the request
// did not complete (no credential, a transport failure, or a body that could not
// be read) and the buffer holds a fixed reason, a failure the guest may handle.
// Every other failure (a refused route, an oversized response, a pointer outside
// memory) is fatal: the call is aborted and the Connector closed.
//
// http_response_header(namePtr, nameLen, outPtr, outCap i32) -> i64 reads one
// header of the last http_request's response, so a guest that never imports it
// is unchanged. The result is found<<32 | n: found is 1 for a present header
// (an empty value is present) and 0 for an absent one, n is the value's full
// length, and min(n, outCap) bytes of it are written at outPtr, so a guest
// whose buffer was short asks again with a bigger one. Repeated values are
// joined with ", " and the name is case-insensitive. Set-Cookie is never
// served, and no request leaves a header behind: a refused, failed or oversized
// one makes every name absent. A pointer outside memory or a name over
// maxHeaderName is fatal, as for http_request.
func (c *Connector) instantiateNetwork(ctx context.Context) error {
	i32, i64 := api.ValueTypeI32, api.ValueTypeI64
	_, err := c.rt.NewHostModuleBuilder(hostModule).NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			stack[0] = c.httpRequest(ctx, mod.Memory(), api.DecodeU32(stack[0]), api.DecodeU32(stack[1]), api.DecodeU32(stack[2]), api.DecodeU32(stack[3]))
		}), []api.ValueType{i32, i32, i32, i32}, []api.ValueType{i64}).
		Export(hostRequest).
		NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, mod api.Module, stack []uint64) {
			stack[0] = c.httpResponseHeader(mod.Memory(), api.DecodeU32(stack[0]), api.DecodeU32(stack[1]), api.DecodeU32(stack[2]), api.DecodeU32(stack[3]))
		}), []api.ValueType{i32, i32, i32, i32}, []api.ValueType{i64}).
		Export(hostResponseHeader).Instantiate(ctx)
	return err
}

// fatal stashes err for invoke, which reports it instead of the trap the panic
// causes: wazero turns a host function's panic into the guest call's error and
// unwinds the guest.
func (c *Connector) fatal(err error) uint64 {
	c.hostErr = err
	panic(err)
}

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
	err := fmt.Errorf("%w: connector %q: %s.%s: %s", ErrMalformedOutput, h.n.Name, hostModule, call, why)
	// No request completed, so this is the call's one audit entry.
	h.n.recordFailure(DecisionRefused, err)
	h.failed = true
	return err
}

// malformed stops the guest with the error the handler refused the named
// import with.
func (c *Connector) malformed(call, why string) uint64 {
	return c.fatal(c.host.refuse(call, why))
}

// httpRequest serves one http_request.
func (c *Connector) httpRequest(ctx context.Context, mem api.Memory, reqPtr, reqLen, respPtr, respCap uint32) uint64 {
	c.respHeader = nil
	if reqLen > maxRequest {
		return c.malformed(hostRequest, fmt.Sprintf("request of %d bytes, cap %d", reqLen, maxRequest))
	}
	raw, ok := mem.Read(reqPtr, reqLen)
	if !ok {
		return c.malformed(hostRequest, "request points outside module memory")
	}
	resp, ok := mem.Read(respPtr, respCap)
	if !ok {
		return c.malformed(hostRequest, "response buffer points outside module memory")
	}
	status, body, header, err := c.host.httpRequest(ctx, raw, int(respCap))
	if err != nil {
		return c.fatal(err)
	}
	c.respHeader = header
	// resp is a view of guest memory; copy truncates to respCap.
	return uint64(status)<<32 | uint64(copy(resp, body))
}

// httpResponseHeader serves one http_response_header.
func (c *Connector) httpResponseHeader(mem api.Memory, namePtr, nameLen, outPtr, outCap uint32) uint64 {
	if nameLen > maxHeaderName {
		return c.malformed(hostResponseHeader, fmt.Sprintf("header name of %d bytes, cap %d", nameLen, maxHeaderName))
	}
	name, ok := mem.Read(namePtr, nameLen)
	if !ok {
		return c.malformed(hostResponseHeader, "header name points outside module memory")
	}
	out, ok := mem.Read(outPtr, outCap)
	if !ok {
		return c.malformed(hostResponseHeader, "header buffer points outside module memory")
	}
	values := c.respHeader.Values(http.CanonicalHeaderKey(string(name)))
	if len(values) == 0 {
		return 0
	}
	v := strings.Join(values, ", ")
	copy(out, v)
	return 1<<32 | uint64(len(v))
}
