// Package connector is the Connector author's side of the Connector protocol
// (version 0.4.0, ADR 0073): it hides the wasm export plumbing and the JSON
// encoding so an author only declares capabilities.
//
// A Connector is built as a wasip1 reactor
// (GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared). The host runs
// _initialize, which runs package inits but never main.main, so a Connector
// registers from init().
package connector

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
)

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

var (
	handlers    map[string]func(json.RawMessage) (any, error)
	rawHandlers map[string]func([]byte) (any, error)
)

func mustBeUnregistered(method string) {
	_, dup := handlers[method]
	if _, raw := rawHandlers[method]; dup || raw {
		panic("connector: duplicate handler for method " + strconv.Quote(method))
	}
}

// Handle registers fn as the handler for method, called through the host's
// connector_call export. Call it from init(); registering a method twice
// (raw or not) panics.
func Handle(method string, fn func(params json.RawMessage) (any, error)) {
	mustBeUnregistered(method)
	if handlers == nil {
		handlers = map[string]func(json.RawMessage) (any, error){}
	}
	handlers[method] = fn
}

// HandleRaw registers fn for a raw request, which the host frames as
// "<method>\n<payload>" rather than a JSON envelope (every JSON request
// starts with '{', so the first byte tells them apart). fn reads payload in
// place with no JSON decode; payload aliases the call buffer, so fn must not
// retain it past the call. The response is encoded as for Handle. Call it
// from init(); registering a method twice (raw or not) panics.
func HandleRaw(method string, fn func(payload []byte) (any, error)) {
	mustBeUnregistered(method)
	if rawHandlers == nil {
		rawHandlers = map[string]func([]byte) (any, error){}
	}
	rawHandlers[method] = fn
}

type callRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type errorBody struct {
	Error string `json:"error"`
	Class Class  `json:"class"`
}

// call runs one request and returns the response JSON: {"result": ...} on
// success, {"error": "...", "class": "..."} on any failure. A handler error
// reports the class of the *Error in its chain; any other failure (a handler
// error with no class, a malformed request, an unknown method, an encode
// failure) is Permanent. A request whose first byte is not '{' is raw; see
// HandleRaw. The host's JSON encoding never emits leading whitespace, so a
// request with any is raw, not JSON.
func call(req []byte) []byte {
	var (
		res any
		err error
	)
	if len(req) > 0 && req[0] != '{' {
		method, payload, ok := bytes.Cut(req, []byte{'\n'})
		if !ok {
			return errorResponse(Permanent, "malformed request: raw request has no method line")
		}
		fn, ok := rawHandlers[string(method)]
		if !ok {
			return errorUnknownMethod(string(method))
		}
		res, err = fn(payload)
	} else {
		var r callRequest
		if err := json.Unmarshal(req, &r); err != nil {
			return errorResponse(Permanent, "malformed request: "+err.Error())
		}
		fn, ok := handlers[r.Method]
		if !ok {
			return errorUnknownMethod(r.Method)
		}
		res, err = fn(r.Params)
	}
	if err != nil {
		class := Permanent
		if ce := new(*Error); errors.As(err, ce) {
			class = (*ce).Class
		}
		return errorResponse(class, err.Error())
	}
	out, err := json.Marshal(struct {
		Result any `json:"result"`
	}{res})
	if err != nil {
		return errorResponse(Permanent, "encode result: "+err.Error())
	}
	return out
}

func errorUnknownMethod(method string) []byte {
	return errorResponse(Permanent, "unknown method "+strconv.Quote(method))
}

func errorResponse(class Class, msg string) []byte {
	out, err := json.Marshal(errorBody{msg, class})
	if err != nil {
		panic(err) // a string cannot fail to marshal
	}
	return out
}
