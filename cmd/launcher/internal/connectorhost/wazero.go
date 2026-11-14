package connectorhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// wasmEngine is the in-process engine: the module runs in a wazero runtime in
// this process. Its methods are not safe for concurrent use but for abort; the
// Connector's mu serializes them.
type wasmEngine struct {
	rt wazero.Runtime
	// cache is the compilation cache LoadCompiled configured, nil for Load. Its
	// compiled engine outlives rt.Close, so close releases it too.
	cache  wazero.CompilationCache
	mod    api.Module
	mem    api.Memory
	memCap *capAllocator
	// host serves a network Connector's Host calls; nil when the grant is none.
	host hostCalls
	// hostErr is a Host call's fatal failure, set just before it panics to unwind
	// the guest; invoke reports it in place of the trap. Reset by each invoke.
	hostErr error
	// respHeader is the last http_request's response headers, nil when it left
	// none; http_response_header reads it. Reset by each invoke.
	respHeader http.Header
	// closed makes close idempotent: a failed call closes the engine before
	// Connector.Close does.
	closed bool
}

var _ engine = (*wasmEngine)(nil)

// engineFactory builds the engine a Connector runs over from the module and the
// Host calls it is granted, nil for none.
type engineFactory func(ctx context.Context, wasm []byte, host hostCalls) (engine, error)

// interpretedEngine is the engine under wazero's interpreter, not its
// compiler: its per-line cost is well inside the heartbeat's line rate (ADR
// 0073, amendment for #6798).
func interpretedEngine() engineFactory {
	return func(ctx context.Context, wasm []byte, host hostCalls) (engine, error) {
		return newWasmEngine(ctx, wasm, wazero.NewRuntimeConfigInterpreter(), nil, host)
	}
}

// compiledEngine is the engine under wazero's compiler where the platform has
// one, with the compiled module kept in cacheDir when it is not "".
func compiledEngine(cacheDir string) engineFactory {
	return func(ctx context.Context, wasm []byte, host hostCalls) (engine, error) {
		cfg, cache := compiledConfig(cacheDir)
		return newWasmEngine(ctx, wasm, cfg, cache, host)
	}
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

// newWasmEngine compiles and instantiates wasm and runs its _initialize. It
// takes ownership of cache, which may be nil, and releases it with the runtime
// on failure.
func newWasmEngine(ctx context.Context, wasm []byte, cfg wazero.RuntimeConfig, cache wazero.CompilationCache, host hostCalls) (*wasmEngine, error) {
	// Reference types are off because table.grow is uncapped and one
	// uninterruptible host operation. SIMD and multi-value are off so every value
	// and block type takes one slot, which lets checkDeclaredSizes bound a frame's
	// operand stack from the binary; the interpreter's stack has no ceiling of its
	// own. Bulk memory stays on, Go's wasm backend emits memory.copy and
	// memory.fill.
	cfg = cfg.WithCloseOnContextDone(true).
		WithCoreFeatures(api.CoreFeaturesV2 &^ (api.CoreFeatureReferenceTypes | api.CoreFeatureSIMD | api.CoreFeatureMultiValue))
	e := &wasmEngine{
		rt:     wazero.NewRuntimeWithConfig(ctx, cfg),
		cache:  cache,
		memCap: &capAllocator{ceiling: memoryCapBytes},
		host:   host,
	}
	if err := e.load(ctx, wasm); err != nil {
		_ = e.close(context.WithoutCancel(ctx))
		return nil, err
	}
	return e, nil
}

// load compiles and instantiates wasm and runs _initialize.
func (e *wasmEngine) load(ctx context.Context, wasm []byte) error {
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, e.rt); err != nil {
		return err
	}
	if e.host != nil {
		if err := e.instantiateNetwork(ctx); err != nil {
			return err
		}
	}
	if err := checkDeclaredSizes(wasm); err != nil {
		return err
	}
	compiled, err := e.rt.CompileModule(ctx, wasm)
	if err != nil {
		return err
	}
	if e.mod, err = e.instantiate(ctx, compiled); err != nil {
		return err
	}
	// Not mod.Memory(): on a module with no memory it is a non-nil handle on nil.
	e.mem = e.mod.ExportedMemory("memory")
	if e.mod.ExportedFunction("_initialize") != nil {
		initFn, err := e.export("_initialize", nil, nil)
		if err != nil {
			return err
		}
		initCtx, cancel := e.applyLimits(ctx, initLimits)
		defer cancel()
		if _, err := e.invoke(initCtx, initFn, "_initialize"); err != nil {
			return err
		}
	}
	return nil
}

func (e *wasmEngine) handshake(ctx context.Context) ([]byte, error) {
	fn, err := e.export("connector_handshake", nil, []api.ValueType{api.ValueTypeI64})
	if err != nil {
		return nil, err
	}
	lim := limitsFor(0)
	ctx, cancel := e.applyLimits(ctx, lim)
	defer cancel()
	res, err := e.invoke(ctx, fn, "connector_handshake")
	if err != nil {
		return nil, err
	}
	return readOutput(e.mem, "connector_handshake", res[0], lim.output)
}

// call writes req into guest memory and returns the result connector_call
// produced; see exchange.
func (e *wasmEngine) call(ctx context.Context, req []byte) ([]byte, error) {
	alloc, call, err := e.callExports()
	if err != nil {
		return nil, err
	}
	return e.exchange(ctx, alloc, call, req)
}

// abort closes the module, which wazero, built WithCloseOnContextDone, takes as
// a stop to a running guest. mod is set once, in load, so reading it without mu
// is safe.
func (e *wasmEngine) abort() {
	_ = e.mod.CloseWithExitCode(context.Background(), sys.ExitCodeContextCanceled)
}

func (e *wasmEngine) close(ctx context.Context) error {
	if e.closed {
		return nil
	}
	e.closed = true
	err := e.rt.Close(ctx)
	if e.cache != nil {
		err = errors.Join(err, e.cache.Close(ctx))
	}
	return err
}

// applyLimits returns ctx under l's wall-clock budget and raises the memory
// ceiling to l.memory if that is higher; see capAllocator.
func (e *wasmEngine) applyLimits(ctx context.Context, l limits) (context.Context, context.CancelFunc) {
	e.memCap.ceiling = max(e.memCap.ceiling, l.memory)
	return context.WithTimeoutCause(ctx, l.budget, ErrBudget)
}

// invoke runs fn and names how it failed. ctx carries the budget applyLimits set, so
// several invokes can share one.
func (e *wasmEngine) invoke(ctx context.Context, fn api.Function, name string, args ...uint64) ([]uint64, error) {
	e.memCap.hit = false // a refusal the guest survived in an earlier call is not this call's cause
	e.hostErr = nil
	e.respHeader = nil
	res, err := fn.Call(ctx, args...)
	if err == nil {
		return res, nil
	}
	if e.hostErr != nil {
		return nil, fmt.Errorf("%s: %w", name, e.hostErr)
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
	case e.memCap.hit:
		return nil, fmt.Errorf("%s: %w: %w", name, ErrMemoryCap, err)
	}
	return nil, fmt.Errorf("%s: %w: %w", name, ErrTrap, err)
}

// export returns the named export if it has exactly the given signature.
func (e *wasmEngine) export(name string, params, results []api.ValueType) (api.Function, error) {
	fn := e.mod.ExportedFunction(name)
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

// callExports returns connector_alloc and connector_call. A module with
// neither still loads, since a Connector need not answer calls.
func (e *wasmEngine) callExports() (alloc, call api.Function, err error) {
	if alloc, err = e.export("connector_alloc", []api.ValueType{api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}); err != nil {
		return nil, nil, err
	}
	call, err = e.export("connector_call", []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64})
	return alloc, call, err
}

// exchange writes req into guest memory and returns the raw result. The caller
// holds the Connector's mu. connector_alloc and connector_call share one
// budget, so a hostile guest cannot hold the caller for two.
func (e *wasmEngine) exchange(ctx context.Context, alloc, call api.Function, req []byte) ([]byte, error) {
	lim := limitsFor(len(req))
	ctx, cancel := e.applyLimits(ctx, lim)
	defer cancel()
	res, err := e.invoke(ctx, alloc, "connector_alloc", uint64(len(req)))
	if err != nil {
		return nil, err
	}
	ptr := uint32(res[0])
	if !e.mem.Write(ptr, req) {
		return nil, fmt.Errorf("connector_alloc: %w: buffer points outside module memory", ErrMalformedOutput)
	}
	res, err = e.invoke(ctx, call, "connector_call", uint64(ptr), uint64(len(req)))
	if err != nil {
		return nil, err
	}
	return readOutput(e.mem, "connector_call", res[0], lim.output)
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
	if err := checkOutputLen(name, uint64(n), limit); err != nil {
		return nil, err
	}
	raw, ok := mem.Read(ptr, n)
	if !ok {
		return nil, fmt.Errorf("%s: %w: result points outside module memory", name, ErrMalformedOutput)
	}
	if err := checkOutputJSON(name, raw); err != nil {
		return nil, err
	}
	return bytes.Clone(raw), nil
}

// checkOutputLen refuses a result of n bytes over limit.
func checkOutputLen(name string, n, limit uint64) error {
	if n > limit {
		return fmt.Errorf("%s: %w: %d bytes, cap %d", name, ErrOutputTooLarge, n, limit)
	}
	return nil
}

// checkOutputJSON refuses a result that is not valid UTF-8 JSON. The remote
// engine runs it too: the Launcher takes nothing a host sends at face value.
func checkOutputJSON(name string, raw []byte) error {
	// json.Valid accepts invalid UTF-8 inside strings.
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return fmt.Errorf("%s: %w: result is not valid UTF-8 JSON", name, ErrMalformedOutput)
	}
	return nil
}

// instantiate links the module without running a start function (the scan
// refuses a start section, so none is left), so a link failure is told from a
// trap; _initialize runs separately, under a budget.
func (e *wasmEngine) instantiate(ctx context.Context, compiled wazero.CompiledModule) (api.Module, error) {
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
	return e.rt.InstantiateModule(experimental.WithMemoryAllocator(ctx, e.memCap), compiled, wazero.NewModuleConfig().WithStartFunctions())
}

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
func (e *wasmEngine) instantiateNetwork(ctx context.Context) error {
	i32, i64 := api.ValueTypeI32, api.ValueTypeI64
	_, err := e.rt.NewHostModuleBuilder(hostModule).NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			stack[0] = e.httpRequest(ctx, mod.Memory(), api.DecodeU32(stack[0]), api.DecodeU32(stack[1]), api.DecodeU32(stack[2]), api.DecodeU32(stack[3]))
		}), []api.ValueType{i32, i32, i32, i32}, []api.ValueType{i64}).
		Export(hostRequest).
		NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, mod api.Module, stack []uint64) {
			stack[0] = e.httpResponseHeader(mod.Memory(), api.DecodeU32(stack[0]), api.DecodeU32(stack[1]), api.DecodeU32(stack[2]), api.DecodeU32(stack[3]))
		}), []api.ValueType{i32, i32, i32, i32}, []api.ValueType{i64}).
		Export(hostResponseHeader).Instantiate(ctx)
	return err
}

// fatal stashes err for invoke, which reports it instead of the trap the panic
// causes: wazero turns a host function's panic into the guest call's error and
// unwinds the guest.
func (e *wasmEngine) fatal(err error) uint64 {
	e.hostErr = err
	panic(err)
}

// malformed stops the guest with the error the handler refused the named
// import with.
func (e *wasmEngine) malformed(call, why string) uint64 {
	return e.fatal(e.host.refuse(call, why))
}

// httpRequest serves one http_request.
func (e *wasmEngine) httpRequest(ctx context.Context, mem api.Memory, reqPtr, reqLen, respPtr, respCap uint32) uint64 {
	e.respHeader = nil
	if reqLen > maxRequest {
		return e.malformed(hostRequest, fmt.Sprintf("request of %d bytes, cap %d", reqLen, maxRequest))
	}
	raw, ok := mem.Read(reqPtr, reqLen)
	if !ok {
		return e.malformed(hostRequest, "request points outside module memory")
	}
	resp, ok := mem.Read(respPtr, respCap)
	if !ok {
		return e.malformed(hostRequest, "response buffer points outside module memory")
	}
	status, body, header, err := e.host.httpRequest(ctx, raw, int(min(respCap, maxHTTPResponse)))
	if err != nil {
		return e.fatal(err)
	}
	e.respHeader = header
	// resp is a view of guest memory; copy truncates to respCap.
	return uint64(status)<<32 | uint64(copy(resp, body))
}

// httpResponseHeader serves one http_response_header.
func (e *wasmEngine) httpResponseHeader(mem api.Memory, namePtr, nameLen, outPtr, outCap uint32) uint64 {
	if nameLen > maxHeaderName {
		return e.malformed(hostResponseHeader, fmt.Sprintf("header name of %d bytes, cap %d", nameLen, maxHeaderName))
	}
	name, ok := mem.Read(namePtr, nameLen)
	if !ok {
		return e.malformed(hostResponseHeader, "header name points outside module memory")
	}
	out, ok := mem.Read(outPtr, outCap)
	if !ok {
		return e.malformed(hostResponseHeader, "header buffer points outside module memory")
	}
	values := e.respHeader.Values(http.CanonicalHeaderKey(string(name)))
	if len(values) == 0 {
		return 0
	}
	v := strings.Join(values, ", ")
	copy(out, v)
	return 1<<32 | uint64(len(v))
}
