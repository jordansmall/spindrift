// Package connectorhost loads a Connector (ADR 0073), a wasip1 reactor module,
// into a sandbox that grants it nothing and runs its protocol handshake.
package connectorhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// ProtocolVersion is the Connector protocol core speaks.
const ProtocolVersion = "0.1.0"

// currentMinor is the 0.y of ProtocolVersion. Core accepts this minor and the
// one before it, so a Connector survives one core upgrade.
var currentMinor = func() int {
	minor, _ := strconv.Atoi(strings.Split(ProtocolVersion, ".")[1])
	return minor
}()

var (
	// ErrProtocolUnsupported marks a Connector whose protocol version is outside
	// the window core accepts; the concrete error is a *ProtocolError.
	ErrProtocolUnsupported = errors.New("connector protocol unsupported")
	// ErrHandshake marks a module that failed to load or to complete the
	// handshake: not valid wasm, an unlinkable import, a missing export, a wrong
	// signature, no memory export, malformed handshake JSON or a context
	// deadline that passed.
	ErrHandshake = errors.New("connector handshake failed")
	// ErrCall marks a failed connector_call: the module lacks the call exports, a
	// guest call trapped, the exchange was malformed or oversized, the Connector
	// answered with an error, or its result failed host validation (an unknown
	// pattern reason, a ResolveExit answer that breaks the Driver invariant).
	ErrCall = errors.New("connector call failed")
)

// ProtocolError is a Connector protocol version outside core's window.
type ProtocolError struct {
	Connector string // the Connector's protocolVersion as it declared it
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("connector protocol %q is outside core's supported range %s (core speaks %s)",
		e.Connector, supportedRange(), ProtocolVersion)
}

func (e *ProtocolError) Unwrap() error { return ErrProtocolUnsupported }

func supportedRange() string {
	return fmt.Sprintf("0.%d.x to 0.%d.x", currentMinor-1, currentMinor)
}

// CheckProtocol reports whether core accepts a Connector declaring version, a
// strict MAJOR.MINOR.PATCH of decimal digits without leading zeros, major 0.
func CheckProtocol(version string) error {
	parts := strings.Split(version, ".")
	if len(parts) == 3 && parts[0] == "0" && canonical(parts[1]) && canonical(parts[2]) {
		if minor, err := strconv.Atoi(parts[1]); err == nil && (minor == currentMinor || minor == currentMinor-1) {
			return nil
		}
	}
	return &ProtocolError{Connector: version}
}

func canonical(p string) bool {
	return p == "0" || (p != "" && p[0] != '0' && strings.Trim(p, "0123456789") == "")
}

// maxResponse caps a call response so a misbehaving Connector cannot make the
// host copy out arbitrary amounts of guest memory.
const maxResponse = 64 << 20

// Connector is a loaded Connector that completed the handshake.
type Connector struct {
	ProtocolVersion string
	Capabilities    []string

	rt  wazero.Runtime
	mem api.Memory
	// alloc and callFn are nil when the module lacks the call exports.
	alloc, callFn api.Function
	// mu serializes guest calls: a wazero module instance is not safe for
	// concurrent use.
	mu sync.Mutex
}

// Close releases the Connector's runtime.
func (c *Connector) Close(ctx context.Context) error { return c.rt.Close(ctx) }

// call sends one request through connector_call and decodes the response's
// result into result, which may be nil to ignore it. A Connector-reported
// error comes back wrapping ErrCall.
func (c *Connector) call(ctx context.Context, method string, params, result any) error {
	fail := func(err error) error { return fmt.Errorf("%w: %s: %w", ErrCall, method, err) }
	if c.alloc == nil || c.callFn == nil {
		return fail(errors.New("module does not export connector_alloc and connector_call"))
	}
	req, err := json.Marshal(struct {
		Method string `json:"method"`
		Params any    `json:"params"`
	}{method, params})
	if err != nil {
		return fail(err)
	}
	raw, err := c.exchange(ctx, req)
	if err != nil {
		return fail(err)
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *string         `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fail(fmt.Errorf("malformed response: %w", err))
	}
	if resp.Error != nil {
		return fail(errors.New(*resp.Error))
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Result, result); err != nil {
		return fail(fmt.Errorf("decode result: %w", err))
	}
	return nil
}

// exchange writes req into guest memory and returns a copy of the response;
// the guest may reuse its buffer on the next call.
func (c *Connector) exchange(ctx context.Context, req []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	res, err := c.alloc.Call(ctx, uint64(len(req)))
	if err != nil {
		return nil, err
	}
	ptr := uint32(res[0])
	if !c.mem.Write(ptr, req) {
		return nil, errors.New("connector_alloc points outside module memory")
	}
	res, err = c.callFn.Call(ctx, uint64(ptr), uint64(len(req)))
	if err != nil {
		return nil, err
	}
	rptr, n := uint32(res[0]>>32), uint32(res[0])
	if n > maxResponse {
		return nil, fmt.Errorf("response of %d bytes exceeds the %d byte limit", n, maxResponse)
	}
	raw, ok := c.mem.Read(rptr, n)
	if !ok {
		return nil, errors.New("response points outside module memory")
	}
	return bytes.Clone(raw), nil
}

type handshake struct {
	ProtocolVersion string   `json:"protocolVersion"`
	Capabilities    []string `json:"capabilities"`
}

// Load compiles wasm, runs its _initialize and handshake, and refuses a
// protocol version outside core's window.
//
// The sandbox grants nothing: wazero's ModuleConfig defaults to no filesystem,
// environment, arguments or stdio and to fake clocks and a deterministic random
// source, and WASI is the only host module instantiated, so a Connector
// importing anything else fails to link.
func Load(ctx context.Context, wasm []byte) (*Connector, error) {
	// The interpreter, not wazero's compiler: whether the compiler earns its
	// complexity is decided later from measured per-line cost.
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigInterpreter().WithCloseOnContextDone(true))
	c, err := instantiate(ctx, rt, wasm)
	if err == nil {
		err = CheckProtocol(c.ProtocolVersion)
	}
	if err != nil {
		_ = rt.Close(ctx)
		return nil, err
	}
	return c, nil
}

// instantiate runs the module's start function and handshake and returns the
// Connector holding its module state.
func instantiate(ctx context.Context, rt wazero.Runtime, wasm []byte) (*Connector, error) {
	fail := func(err error) (*Connector, error) { return nil, fmt.Errorf("%w: %w", ErrHandshake, err) }
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		return fail(err)
	}
	compiled, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		return fail(err)
	}
	mod, err := rt.InstantiateModule(ctx, compiled,
		wazero.NewModuleConfig().WithStartFunctions("_initialize"))
	if err != nil {
		return fail(err)
	}
	fn := mod.ExportedFunction("connector_handshake")
	if fn == nil {
		return fail(errors.New("module does not export connector_handshake"))
	}
	def := fn.Definition()
	if len(def.ParamTypes()) != 0 || len(def.ResultTypes()) != 1 || def.ResultTypes()[0] != api.ValueTypeI64 {
		return fail(errors.New("connector_handshake must take no parameters and return one i64"))
	}
	// Not mod.Memory(): on a module with no memory it is a non-nil handle on nil.
	mem := mod.ExportedMemory("memory")
	if mem == nil {
		return fail(errors.New("module does not export memory"))
	}
	res, err := fn.Call(ctx)
	if err != nil {
		return fail(err)
	}
	ptr, n := uint32(res[0]>>32), uint32(res[0])
	raw, ok := mem.Read(ptr, n)
	if !ok {
		return fail(errors.New("handshake points outside module memory"))
	}
	var h handshake
	if err := json.Unmarshal(raw, &h); err != nil {
		return fail(err)
	}
	return &Connector{
		ProtocolVersion: h.ProtocolVersion,
		Capabilities:    h.Capabilities,
		rt:              rt,
		mem:             mem,
		alloc:           mod.ExportedFunction("connector_alloc"),
		callFn:          mod.ExportedFunction("connector_call"),
	}, nil
}
