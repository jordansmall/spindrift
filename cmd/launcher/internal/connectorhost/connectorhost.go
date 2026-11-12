// Package connectorhost loads a Connector (ADR 0073), a wasip1 reactor module,
// into a sandbox that grants it nothing and runs its protocol handshake.
package connectorhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// ProtocolVersion is the Connector protocol core speaks.
const ProtocolVersion = "0.2.0"

// Limits on every call into a Connector. The bases below are wide multiples of
// what a Go reactor needs in the interpreter, because Nix CI runs under load;
// a connector_call adds an allowance per byte of the request the host sends
// (limitsFor), since a correct Connector's time, memory and output grow with its
// input, while a hostile one is still bounded by what the host chose to send.
const (
	callBudget = 5 * time.Second
	// initBudget covers _initialize, which boots Go's runtime inside the
	// interpreter.
	initBudget     = 30 * time.Second
	memoryCapBytes = 64 << 20
	// maxOutput bounds one result, a call response or the handshake; it is checked
	// before the bytes are read.
	maxOutput = 8 << 20
	// maxRequest bounds one connector_call request, refused before the guest sees
	// it. It covers a 4 MiB logscan line, the most the launcher reads: a valid
	// JSON line at most doubles in encoding (HTML escaping is off, so only " and
	// \ grow), though control bytes and invalid UTF-8 grow up to 6x. It bounds every
	// scaled limit below: at maxRequest a call gets at most 5 s + 8 min, 192 MiB
	// of memory and 56 MiB of output, so a looping or leaking Connector cannot
	// be handed more by a large request.
	maxRequest = 8 << 20
	// The allowances per request byte, sized from the fixture Driver's decode under
	// the interpreter: time is linear in request bytes at 4 to 11 s per MiB, peak
	// guest memory is at most ~9 bytes per request byte, and a response is about
	// as long as its request but at most 6 times it, since the JSON escape \u00XX
	// stands for one request byte. The time and memory allowances keep 5x and 2x
	// headroom.
	budgetPerMiB         = 60 * time.Second
	memoryPerRequestByte = 16
	outputPerRequestByte = 6
	// maxTableEntries bounds the tables' declared minimums together, since wazero
	// allocates them in full at instantiation. A Go reactor declares ~6000 entries.
	maxTableEntries = 1 << 16
	// maxFrameSlots bounds one function's frame: its parameters, declared locals
	// and the most operands its stack holds. The interpreter keeps all three on
	// one growing stack, up to its 2000-frame call stack, so 2000 frames of this
	// many 8-byte slots stay near memoryCapBytes even after the slice doubles.
	// The fixture Go reactors need at most 27 slots in a frame.
	maxFrameSlots = 1 << 11
	// maxModuleLocals bounds every function's declared locals together, since
	// wazero sizes them all at compile time. The fixture Go reactors declare
	// 8000 to 12000 in total.
	maxModuleLocals = 1 << 20
)

// limits are the ceilings on one call into a Connector.
type limits struct {
	budget time.Duration
	memory uint64 // bytes of module memory
	output uint64 // bytes of result
}

// initLimits are the limits of _initialize, which sends no request.
var initLimits = limits{budget: initBudget, memory: memoryCapBytes, output: maxOutput}

// limitsFor returns the ceilings for a call whose request is requestBytes long;
// the handshake, which sends none, gets the bases. requestBytes never exceeds
// maxRequest, so the Duration cannot overflow.
func limitsFor(requestBytes int) limits {
	n := uint64(requestBytes)
	return limits{
		budget: callBudget + time.Duration(n)*budgetPerMiB>>20,
		memory: memoryCapBytes + n*memoryPerRequestByte,
		output: maxOutput + n*outputPerRequestByte,
	}
}

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
	// handshake. It wraps the specific cause, which still matches its own
	// sentinel (ErrBudget, ErrTrap, ErrMemoryCap, ErrDeclaredSize, ErrExport,
	// ErrOutputTooLarge, ErrMalformedOutput); a failure with none, such as invalid
	// wasm or an unlinkable import, matches only this. A context deadline that
	// passed also matches context.DeadlineExceeded. A Host call that fails closed
	// during the handshake (see ErrCall) surfaces here with its own sentinel.
	ErrHandshake = errors.New("connector handshake failed")
	// ErrCall marks a failed connector_call. It wraps the specific cause, which
	// still matches its own sentinel: ErrBudget, ErrTrap, ErrMemoryCap,
	// ErrOutputTooLarge, ErrMalformedOutput (a malformed exchange, response or
	// result, or a Host call with pointers outside module memory), ErrExport (the
	// module lacks the call exports or gives them the wrong signature), ErrUnusable,
	// or, for a network Connector, ErrRouteRefused, ErrResponseTooLarge or ErrAudit (a Host
	// call failed closed; such a cause is not also ErrTrap). A call the caller's
	// context cut off also matches that context's error. Every such guest failure
	// but ErrExport closes the Connector. A Connector that answered with an error,
	// or whose result failed the Driver's host-side validation (an unknown pattern
	// reason, a ResolveExit answer that breaks the invariant), matches only ErrCall
	// and stays usable; so does a request refused as ErrRequestTooLarge, which
	// never reaches the guest.
	ErrCall = errors.New("connector call failed")
	// ErrExport marks an export that is missing or lacks the signature the ABI
	// gives it: connector_handshake takes nothing and returns an i64,
	// connector_alloc takes an i32 and returns an i32, connector_call takes two
	// i32 and returns an i64. It also marks a start section: the ABI initialises
	// through _initialize, under a budget, so a start function, which would run
	// unbudgeted at instantiation, is refused.
	ErrExport = errors.New("connector export missing or of the wrong signature")
	// ErrBudget marks a call that ran past its wall-clock budget. wazero has no
	// instruction budget, so wall-clock time is the only bound on a looping guest.
	ErrBudget = errors.New("connector call exceeded its time budget")
	// ErrTrap marks a module that trapped or exited during a call. Go's wasip1
	// runtime turns a panic into proc_exit(2), so a panic surfaces here too.
	ErrTrap = errors.New("connector trapped")
	// ErrMemoryCap marks a module that tried to grow its memory past the cap, or
	// declared a memory minimum above it.
	ErrMemoryCap = errors.New("connector exceeded its memory cap")
	// ErrDeclaredSize marks a module that declares tables, vectors, or function
	// frames (parameters, locals and operand-stack height) beyond the host's caps,
	// or a vector count beyond the bytes left in its section; it is refused before
	// it compiles. Other structural problems the size scan finds, such as a section
	// length past the binary or a malformed LEB128, match only ErrHandshake.
	ErrDeclaredSize = errors.New("connector declares tables, frames or vectors beyond the host's caps")
	// ErrOutputTooLarge marks a result whose length is over the output cap.
	ErrOutputTooLarge = errors.New("connector output too large")
	// ErrRequestTooLarge marks a request over maxRequest. The host refuses it
	// before the guest sees it, so the Connector stays usable; a Driver degrades
	// the refused call (a decode line becomes KindUnparsed).
	ErrRequestTooLarge = errors.New("connector request too large")
	// ErrMalformedOutput marks output the host cannot take at face value: a
	// result or buffer pointing outside module memory, a result that is not valid
	// UTF-8 JSON, a response that is not a {result, error} envelope, or a result
	// that does not decode into the caller's value. The handshake result must also
	// carry its required fields.
	ErrMalformedOutput = errors.New("connector output malformed")
	// ErrUnusable marks a Connector that already failed a call or was closed:
	// its module is gone, so every further call is refused.
	ErrUnusable = errors.New("connector unusable")
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

// ProtocolMinor returns the minor of a strict MAJOR.MINOR.PATCH version of
// decimal digits without leading zeros, major 0; ok is false for anything else.
func ProtocolMinor(version string) (minor int, ok bool) {
	parts := strings.Split(version, ".")
	if len(parts) != 3 || parts[0] != "0" || !canonical(parts[1]) || !canonical(parts[2]) {
		return 0, false
	}
	minor, err := strconv.Atoi(parts[1])
	return minor, err == nil
}

// CheckProtocol reports whether core accepts a Connector declaring version: a
// valid ProtocolMinor equal to core's or the one before it.
func CheckProtocol(version string) error {
	if minor, ok := ProtocolMinor(version); ok && (minor == currentMinor || minor == currentMinor-1) {
		return nil
	}
	return &ProtocolError{Connector: version}
}

func canonical(p string) bool {
	return p == "0" || (p != "" && p[0] != '0' && strings.Trim(p, "0123456789") == "")
}

// Connector is a loaded Connector that completed the handshake. It is safe for
// concurrent use: calls into its module are serialized.
type Connector struct {
	ProtocolVersion string
	Capabilities    []string

	rt wazero.Runtime
	// cache is the compilation cache LoadCompiled configured, nil for Load. Its
	// compiled engine outlives rt.Close, so Close releases it too.
	cache  wazero.CompilationCache
	mod    api.Module
	mem    api.Memory
	memCap *capAllocator
	// network is the grant LoadNetwork gave, nil for Load.
	network *Network
	// hostErr is a Host call's fatal failure, set just before it panics to unwind
	// the guest; invoke reports it in place of the trap, and call reads it to
	// tell an already-audited failure from one it must audit. Reset by each
	// invoke. Guarded by mu.
	hostErr error
	// mu serializes guest calls, since a wazero module instance is not safe for
	// concurrent use, and guards dead.
	mu   sync.Mutex
	dead bool // a call failed or Close ran: the module is gone
	// err is the failure that killed the module; nil after a plain Close.
	err error
}

// Err returns the call failure that made the Connector unusable, nil while it
// is usable or after only Close. It exists for callers whose guest-facing API
// has no error return, such as a Decoder, which degrades a failed call to an
// unparsed event.
func (c *Connector) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close releases the Connector's runtime and compilation cache; later calls
// return ErrUnusable.
func (c *Connector) Close(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dead = true
	err := c.rt.Close(ctx)
	if c.cache != nil {
		err = errors.Join(err, c.cache.Close(ctx))
	}
	return err
}

// Abort kills the module without taking mu, so it ends a call another goroutine
// is inside; Close cannot, since call holds mu for the whole exchange. wazero,
// built WithCloseOnContextDone, stops a running guest once its module closes,
// and that call then fails and marks the Connector dead as any other failure.
// mod is set once, in load, so reading it here without mu is safe.
func (c *Connector) Abort() {
	_ = c.mod.CloseWithExitCode(context.Background(), sys.ExitCodeContextCanceled)
}

// applyLimits returns ctx under l's wall-clock budget and raises the memory
// ceiling to l.memory if that is higher; see capAllocator.
func (c *Connector) applyLimits(ctx context.Context, l limits) (context.Context, context.CancelFunc) {
	c.memCap.ceiling = max(c.memCap.ceiling, l.memory)
	return context.WithTimeoutCause(ctx, l.budget, ErrBudget)
}

// invoke runs fn and names how it failed. ctx carries the budget applyLimits set, so
// several invokes can share one.
func (c *Connector) invoke(ctx context.Context, fn api.Function, name string, args ...uint64) ([]uint64, error) {
	c.memCap.hit = false // a refusal the guest survived in an earlier call is not this call's cause
	c.hostErr = nil
	res, err := fn.Call(ctx, args...)
	if err == nil {
		return res, nil
	}
	if c.hostErr != nil {
		return nil, fmt.Errorf("%s: %w", name, c.hostErr)
	}
	// With WithCloseOnContextDone a call cut off by its context surfaces as a
	// sys.ExitError and closes the module, so the context is checked before the
	// error is taken for a trap. A memory-cap refusal is a trap too (Go's runtime
	// throws "out of memory" -> proc_exit(2)) and is told apart by the allocator.
	switch {
	case errors.Is(context.Cause(ctx), ErrBudget):
		return nil, fmt.Errorf("%s: %w: %w", name, ErrBudget, err)
	case ctx.Err() != nil:
		return nil, fmt.Errorf("%s: %w: %w", name, ctx.Err(), err)
	case c.memCap.hit:
		return nil, fmt.Errorf("%s: %w: %w", name, ErrMemoryCap, err)
	}
	return nil, fmt.Errorf("%s: %w: %w", name, ErrTrap, err)
}

// export returns the named export if it has exactly the given signature.
func (c *Connector) export(name string, params, results []api.ValueType) (api.Function, error) {
	fn := c.mod.ExportedFunction(name)
	if fn == nil {
		return nil, fmt.Errorf("%s: %w: not exported", name, ErrExport)
	}
	def := fn.Definition()
	if !slices.Equal(def.ParamTypes(), params) || !slices.Equal(def.ResultTypes(), results) {
		return nil, fmt.Errorf("%s: %w: must have signature %s", name, ErrExport, signature(params, results))
	}
	return fn, nil
}

func signature(params, results []api.ValueType) string {
	names := func(ts []api.ValueType) string {
		s := make([]string, len(ts))
		for i, t := range ts {
			s[i] = api.ValueTypeName(t)
		}
		return strings.Join(s, ", ")
	}
	return "(" + names(params) + ") -> (" + names(results) + ")"
}

// memoryReader is the slice of api.Memory readOutput needs, so tests can fuzz
// it against a fake memory.
type memoryReader interface {
	Read(offset, byteCount uint32) ([]byte, bool)
}

// readOutput reads the ptr<<32|len result of a guest call: the length is checked
// before the read, the bytes must be valid UTF-8 JSON, and the copy is the
// caller's since the guest may reuse its buffer on the next call.
func readOutput(mem memoryReader, name string, packed, limit uint64) ([]byte, error) {
	ptr, n := uint32(packed>>32), uint32(packed)
	// Before mem.Read: the length is the guest's to choose.
	if uint64(n) > limit {
		return nil, fmt.Errorf("%s: %w: %d bytes, cap %d", name, ErrOutputTooLarge, n, limit)
	}
	raw, ok := mem.Read(ptr, n)
	if !ok {
		return nil, fmt.Errorf("%s: %w: result points outside module memory", name, ErrMalformedOutput)
	}
	// json.Valid accepts invalid UTF-8 inside strings.
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return nil, fmt.Errorf("%s: %w: result is not valid UTF-8 JSON", name, ErrMalformedOutput)
	}
	return bytes.Clone(raw), nil
}

// capAllocator backs module memory with a hard byte cap. wazero checks a grow
// against the module's declared maximum before it reaches the allocator and Go
// modules declare none, so the cap lives here, and hit is how a refusal is told
// from an ordinary trap. ceiling is the highest any call so far was allowed;
// the guest's memory never shrinks, so a lower one would refuse a small call
// even one page of growth after a large call.
type capAllocator struct {
	hit     bool
	ceiling uint64
}

func (a *capAllocator) Allocate(_, _ uint64) experimental.LinearMemory {
	return &capMemory{a: a}
}

type capMemory struct {
	a   *capAllocator
	buf []byte
}

func (m *capMemory) Reallocate(size uint64) []byte {
	if size > m.a.ceiling {
		m.a.hit = true
		return nil
	}
	if size > uint64(cap(m.buf)) {
		grown := make([]byte, size, max(size, min(2*uint64(cap(m.buf)), m.a.ceiling)))
		copy(grown, m.buf)
		m.buf = grown
	}
	// Bytes past len were never exposed to the guest, so they are still zero.
	m.buf = m.buf[:size]
	return m.buf
}

func (m *capMemory) Free() { m.buf = nil }

// encodeRequest is the one encoding of a connector_call request, so the size
// check in call and logWindow measure the same bytes.
func encodeRequest(method string, params any) ([]byte, error) {
	// No HTML escaping: <, > and & would each grow 6x. Encode appends a newline;
	// trim it so the bytes match json.Marshal's.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(struct {
		Method string `json:"method"`
		Params any    `json:"params"`
	}{method, params}); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// Call sends one request through connector_call and decodes the response's
// result into result, which may be nil to ignore it. A failure comes back
// wrapping ErrCall and, for a guest failure, its own sentinel too.
//
// Any failure of the exchange itself, or of the response it returned, leaves
// the module in an unknown state: the Connector is closed and every later call
// returns ErrUnusable. A Connector-reported error is an answer, not a failure,
// and does not.
func (c *Connector) Call(ctx context.Context, method string, params, result any) error {
	fail := func(err error) error { return fmt.Errorf("%w: %s: %w", ErrCall, method, err) }
	req, err := encodeRequest(method, params)
	if err != nil {
		return fail(err)
	}
	if len(req) > maxRequest {
		return fail(fmt.Errorf("%w: %d bytes, cap %d", ErrRequestTooLarge, len(req), maxRequest))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead {
		return fail(ErrUnusable)
	}
	alloc, call, err := c.callExports()
	if err != nil {
		return fail(err)
	}
	resp, err := c.exchange(ctx, alloc, call, req)
	if err == nil {
		err = decodeResult(resp, result)
	}
	if err != nil {
		c.dead = true
		c.err = fail(err)
		// A Host call's fatal failure was audited where it happened; any other
		// failed call (a rejected result, a trap, a budget or memory limit) has not.
		if c.network != nil && c.hostErr == nil {
			c.network.recordFailure(DecisionFailed, c.err)
		}
		// Not ctx: a cancelled caller must not stop the module being released.
		_ = c.rt.Close(context.WithoutCancel(ctx))
		return c.err
	}
	if resp.Error != nil {
		return fail(errors.New(*resp.Error))
	}
	return nil
}

// callExports returns connector_alloc and connector_call. A module with
// neither still loads, since a Connector need not answer calls.
func (c *Connector) callExports() (alloc, call api.Function, err error) {
	if alloc, err = c.export("connector_alloc", []api.ValueType{api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}); err != nil {
		return nil, nil, err
	}
	call, err = c.export("connector_call", []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64})
	return alloc, call, err
}

type response struct {
	Result json.RawMessage `json:"result"`
	Error  *string         `json:"error"`
}

// exchange writes req into guest memory and returns the decoded response. The
// caller holds mu. connector_alloc and connector_call share one budget, so
// a hostile guest cannot hold the caller for two.
func (c *Connector) exchange(ctx context.Context, alloc, call api.Function, req []byte) (*response, error) {
	lim := limitsFor(len(req))
	ctx, cancel := c.applyLimits(ctx, lim)
	defer cancel()
	res, err := c.invoke(ctx, alloc, "connector_alloc", uint64(len(req)))
	if err != nil {
		return nil, err
	}
	ptr := uint32(res[0])
	if !c.mem.Write(ptr, req) {
		return nil, fmt.Errorf("connector_alloc: %w: buffer points outside module memory", ErrMalformedOutput)
	}
	res, err = c.invoke(ctx, call, "connector_call", uint64(ptr), uint64(len(req)))
	if err != nil {
		return nil, err
	}
	raw, err := readOutput(c.mem, "connector_call", res[0], lim.output)
	if err != nil {
		return nil, err
	}
	return decodeResponse(raw)
}

// decodeResponse decodes the {result, error} envelope connector_call returned.
func decodeResponse(raw []byte) (*response, error) {
	var resp response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("connector_call: %w: malformed response: %w", ErrMalformedOutput, err)
	}
	return &resp, nil
}

// decodeResult decodes a successful response's result into result; a
// Connector-reported error or a nil result has nothing to decode.
func decodeResult(resp *response, result any) error {
	if resp.Error != nil || result == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Result, result); err != nil {
		return fmt.Errorf("decode result: %w: %w", ErrMalformedOutput, err)
	}
	return nil
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
// importing anything else fails to link. A Driver Connector stays granted no
// Host calls (ADR 0073 rule 2): only a network Connector loaded with LoadNetwork
// gets spindrift.http_request. Memory and
// table minimums and each function frame's slots (parameters, locals and
// operand stack) are capped, reference types, SIMD and multi-value are off (so
// table.grow does not compile) and every call, _initialize included, runs under
// a wall-clock budget. A call's budget, memory cap and output cap each grow with
// the size of its request (limitsFor), from the bases above.
func Load(ctx context.Context, wasm []byte) (*Connector, error) {
	// The interpreter, not wazero's compiler: its per-line cost is well inside
	// the heartbeat's line rate (ADR 0073, amendment for #6798).
	return loadWith(ctx, wasm, wazero.NewRuntimeConfigInterpreter(), nil, nil)
}

// LoadNetwork is Load for a network Connector (ADR 0073): it also links the one
// Host call spindrift.http_request, served by n, which must name the Connector,
// carry a valid Route table and have an Audit log. A config that does not is
// refused before the module compiles, with ErrRouteTable for the routes. The
// interpreter runs it as in Load.
func LoadNetwork(ctx context.Context, wasm []byte, n Network) (*Connector, error) {
	if err := checkGrant(n); err != nil {
		return nil, err
	}
	return loadWith(ctx, wasm, wazero.NewRuntimeConfigInterpreter(), nil, &n)
}

// checkGrant refuses a network grant before any module compiles for it.
func checkGrant(n Network) error {
	if n.Name == "" || n.Audit == nil {
		return errors.New("connector network grant needs a name and an audit log")
	}
	return ValidateRoutes(n.Routes)
}

// LoadCompiled is Load under wazero's compiler where the platform has one,
// trading a slower load for much cheaper guest calls. It is for test binaries
// that push many lines through one Connector; the launcher uses Load (ADR 0073,
// amendment for #6798).
// A non-empty cacheDir keeps the compiled module on disk, shared across
// processes; "" disables it.
func LoadCompiled(ctx context.Context, wasm []byte, cacheDir string) (*Connector, error) {
	cfg, cache := compiledConfig(cacheDir)
	return loadWith(ctx, wasm, cfg, cache, nil)
}

// LoadNetworkCompiled is LoadNetwork under the compiler, as LoadCompiled is to
// Load: for test binaries that drive a network Connector hard. The grant is
// checked as in LoadNetwork, before the module compiles.
func LoadNetworkCompiled(ctx context.Context, wasm []byte, n Network, cacheDir string) (*Connector, error) {
	if err := checkGrant(n); err != nil {
		return nil, err
	}
	cfg, cache := compiledConfig(cacheDir)
	return loadWith(ctx, wasm, cfg, cache, &n)
}

// compiledConfig returns wazero's compiler config and its compilation cache,
// nil when cacheDir is "".
func compiledConfig(cacheDir string) (wazero.RuntimeConfig, wazero.CompilationCache) {
	cfg := wazero.NewRuntimeConfig()
	if cacheDir == "" {
		return cfg, nil
	}
	// The cache only speeds up the load, so a failure to build one is not a
	// load failure.
	cc, err := wazero.NewCompilationCacheWithDir(cacheDir)
	if err != nil {
		return cfg, nil
	}
	return cfg.WithCompilationCache(cc), cc
}

// loadWith takes ownership of cache, which may be nil. network is the Host call grant, nil for none.
func loadWith(ctx context.Context, wasm []byte, cfg wazero.RuntimeConfig, cache wazero.CompilationCache, network *Network) (*Connector, error) {
	// Reference types are off because table.grow is uncapped and one
	// uninterruptible host operation. SIMD and multi-value are off so every value
	// and block type takes one slot, which lets checkDeclaredSizes bound a frame's
	// operand stack from the binary; the interpreter's stack has no ceiling of its
	// own. Bulk memory stays on, Go's wasm backend emits memory.copy and
	// memory.fill.
	cfg = cfg.WithCloseOnContextDone(true).
		WithCoreFeatures(api.CoreFeaturesV2 &^ (api.CoreFeatureReferenceTypes | api.CoreFeatureSIMD | api.CoreFeatureMultiValue))
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	c := &Connector{rt: rt, memCap: &capAllocator{ceiling: memoryCapBytes}, network: network}
	h, err := c.load(ctx, wasm)
	if err == nil {
		err = CheckProtocol(h.ProtocolVersion)
	}
	if err != nil {
		_ = rt.Close(context.WithoutCancel(ctx))
		if cache != nil {
			_ = cache.Close(context.WithoutCancel(ctx))
		}
		return nil, err
	}
	c.ProtocolVersion, c.Capabilities, c.cache = h.ProtocolVersion, h.Capabilities, cache
	return c, nil
}

// load compiles and instantiates wasm, runs _initialize, then the handshake.
func (c *Connector) load(ctx context.Context, wasm []byte) (*handshake, error) {
	fail := func(err error) (*handshake, error) { return nil, fmt.Errorf("%w: %w", ErrHandshake, err) }
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, c.rt); err != nil {
		return fail(err)
	}
	if c.network != nil {
		if err := c.instantiateNetwork(ctx); err != nil {
			return fail(err)
		}
	}
	if err := checkDeclaredSizes(wasm); err != nil {
		return fail(err)
	}
	compiled, err := c.rt.CompileModule(ctx, wasm)
	if err != nil {
		return fail(err)
	}
	if c.mod, err = c.instantiate(ctx, compiled); err != nil {
		return fail(err)
	}
	// Not mod.Memory(): on a module with no memory it is a non-nil handle on nil.
	c.mem = c.mod.ExportedMemory("memory")
	if c.mod.ExportedFunction("_initialize") != nil {
		initFn, err := c.export("_initialize", nil, nil)
		if err != nil {
			return fail(err)
		}
		initCtx, cancel := c.applyLimits(ctx, initLimits)
		defer cancel()
		if _, err := c.invoke(initCtx, initFn, "_initialize"); err != nil {
			return fail(err)
		}
	}
	fn, err := c.export("connector_handshake", nil, []api.ValueType{api.ValueTypeI64})
	if err != nil {
		return fail(err)
	}
	lim := limitsFor(0)
	hsCtx, cancel := c.applyLimits(ctx, lim)
	defer cancel()
	res, err := c.invoke(hsCtx, fn, "connector_handshake")
	if err != nil {
		return fail(err)
	}
	raw, err := readOutput(c.mem, "connector_handshake", res[0], lim.output)
	if err != nil {
		return fail(err)
	}
	h, err := decodeHandshake(raw)
	if err != nil {
		return fail(err)
	}
	return h, nil
}

// decodeHandshake decodes the handshake result; protocolVersion and
// capabilities are both required.
func decodeHandshake(raw []byte) (*handshake, error) {
	var h handshake
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, fmt.Errorf("connector_handshake: %w: %w", ErrMalformedOutput, err)
	}
	// null and {} decode cleanly, so check the fields; capabilities is [] when
	// empty because the SDK always emits it.
	if h.ProtocolVersion == "" || h.Capabilities == nil {
		return nil, fmt.Errorf("connector_handshake: %w: protocolVersion and capabilities are required", ErrMalformedOutput)
	}
	return &h, nil
}

// instantiate links the module without running a start function (the scan
// refuses a start section, so none is left), so a link failure is told from a
// trap; _initialize runs separately, under a budget.
func (c *Connector) instantiate(ctx context.Context, compiled wazero.CompiledModule) (api.Module, error) {
	// Checked on the compiled module because an over-cap initial size reaching
	// the allocator makes wazero panic with a slice-bounds error. A module has at
	// most one memory (wazero's binary decoder rejects more), so requiring it
	// exported leaves no unexported one for the allocator to see; an imported
	// memory cannot link, WASI exporting none.
	def, ok := compiled.ExportedMemories()["memory"]
	if !ok {
		return nil, fmt.Errorf("%w: module does not export memory", ErrExport)
	}
	if uint64(def.Min())<<16 > memoryCapBytes {
		return nil, fmt.Errorf("%w: declared minimum of %d pages exceeds the %d byte cap", ErrMemoryCap, def.Min(), memoryCapBytes)
	}
	return c.rt.InstantiateModule(experimental.WithMemoryAllocator(ctx, c.memCap), compiled, wazero.NewModuleConfig().WithStartFunctions())
}
