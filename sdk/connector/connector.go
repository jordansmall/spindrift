// Package connector is the Connector author's side of the Connector protocol
// (version 0.3.0, ADR 0073): it hides the wasm export plumbing and the JSON
// encoding so an author only declares capabilities.
//
// A Connector is built as a wasip1 reactor
// (GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared). The host runs
// _initialize, which runs package inits but never main.main, so a Connector
// registers from init().
package connector

import (
	"encoding/json"
	"strconv"
)

// protocolVersion is a var, not a const, so a test fixture can override it
// with -ldflags=-X spindrift.dev/sdk/connector.protocolVersion=0.4.0.
var protocolVersion = "0.3.0"

var capabilities []string

// Register declares capabilities the Connector offers. Call it from init().
func Register(caps ...string) {
	capabilities = append(capabilities, caps...)
}

type handshakeResult struct {
	ProtocolVersion string   `json:"protocolVersion"`
	Capabilities    []string `json:"capabilities"`
}

func handshakeJSON() ([]byte, error) {
	caps := capabilities
	if caps == nil {
		caps = []string{} // encode [] rather than null
	}
	return json.Marshal(handshakeResult{protocolVersion, caps})
}

// pack folds a wasm32 linear-memory pointer and length into the single
// uint64 a wasmexport returns: ptr<<32 | len.
func pack(ptr, length uint32) uint64 {
	return uint64(ptr)<<32 | uint64(length)
}

var handlers map[string]func(json.RawMessage) (any, error)

// Handle registers fn as the handler for method, called through the host's
// connector_call export. Call it from init(); registering a method twice
// panics.
func Handle(method string, fn func(params json.RawMessage) (any, error)) {
	if _, dup := handlers[method]; dup {
		panic("connector: duplicate handler for method " + strconv.Quote(method))
	}
	if handlers == nil {
		handlers = map[string]func(json.RawMessage) (any, error){}
	}
	handlers[method] = fn
}

type callRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type errorBody struct {
	Error string `json:"error"`
}

// call runs one request and returns the response JSON: {"result": ...} on
// success, {"error": "..."} on any failure.
func call(req []byte) []byte {
	var r callRequest
	if err := json.Unmarshal(req, &r); err != nil {
		return errorResponse("malformed request: " + err.Error())
	}
	fn, ok := handlers[r.Method]
	if !ok {
		return errorResponse("unknown method " + strconv.Quote(r.Method))
	}
	res, err := fn(r.Params)
	if err != nil {
		return errorResponse(err.Error())
	}
	out, err := json.Marshal(struct {
		Result any `json:"result"`
	}{res})
	if err != nil {
		return errorResponse("encode result: " + err.Error())
	}
	return out
}

func errorResponse(msg string) []byte {
	out, err := json.Marshal(errorBody{msg})
	if err != nil {
		panic(err) // a string cannot fail to marshal
	}
	return out
}
