package connectorhost

import (
	"context"
	"errors"
	"fmt"

	"github.com/tetratelabs/wazero/api"
)

// hostModule and hostRequest name the one Host call a network Connector gets.
const (
	hostModule  = "spindrift"
	hostRequest = "http_request"
)

// instantiateNetwork links spindrift.http_request, so a module importing it
// links; Load never calls it, which keeps a Driver granted no Host calls.
//
// The ABI is http_request(reqPtr, reqLen, respPtr, respCap i32) -> i64. The
// guest points at a JSON httpRequest and at a buffer for the answer; the result
// is status<<32 | n, with the n body bytes at respPtr. Status 0 means the request
// did not complete (no credential, a transport failure, or a body that could not
// be read) and the buffer holds a fixed reason, a failure the guest may handle.
// Every other failure (a refused route, an oversized response, a pointer outside
// memory) is fatal: the call is aborted and the Connector closed.
func (c *Connector) instantiateNetwork(ctx context.Context) error {
	i32, i64 := api.ValueTypeI32, api.ValueTypeI64
	_, err := c.rt.NewHostModuleBuilder(hostModule).NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			stack[0] = c.httpRequest(ctx, mod.Memory(), api.DecodeU32(stack[0]), api.DecodeU32(stack[1]), api.DecodeU32(stack[2]), api.DecodeU32(stack[3]))
		}), []api.ValueType{i32, i32, i32, i32}, []api.ValueType{i64}).
		Export(hostRequest).Instantiate(ctx)
	return err
}

// httpRequest serves one http_request. A fatal failure is stashed for invoke,
// which reports it instead of the trap the panic causes: wazero turns a host
// function's panic into the guest call's error and unwinds the guest.
func (c *Connector) httpRequest(ctx context.Context, mem api.Memory, reqPtr, reqLen, respPtr, respCap uint32) uint64 {
	fatal := func(err error) uint64 {
		c.hostErr = err
		panic(err)
	}
	malformed := func(why string) uint64 {
		err := fmt.Errorf("%w: connector %q: %s.%s: %s", ErrMalformedOutput, c.network.Name, hostModule, hostRequest, why)
		// do never ran, so this is the call's one audit entry.
		c.network.recordFailure(DecisionRefused, err)
		return fatal(err)
	}
	if reqLen > maxRequest {
		return malformed(fmt.Sprintf("request of %d bytes, cap %d", reqLen, maxRequest))
	}
	raw, ok := mem.Read(reqPtr, reqLen)
	if !ok {
		return malformed("request points outside module memory")
	}
	resp, ok := mem.Read(respPtr, respCap)
	if !ok {
		return malformed("response buffer points outside module memory")
	}
	status, body, err := c.network.do(ctx, raw, int(min(respCap, maxHTTPResponse)))
	switch {
	case err == nil:
	case errors.Is(err, ErrHTTPSend):
		// A fixed reason: err carries the request URL and whatever Authorize said,
		// which may be the credential. The detail is in the audit entry. resp is a
		// view of guest memory; copy truncates to respCap.
		return uint64(copy(resp, ErrHTTPSend.Error()))
	default:
		return fatal(err)
	}
	return uint64(status)<<32 | uint64(copy(resp, body))
}
