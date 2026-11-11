// Package connector is the Connector author's side of the Connector ABI
// (protocol 0.1.0, ADR 0073): it hides the wasm export plumbing and the JSON
// encoding so an author only declares capabilities.
//
// A Connector is built as a wasip1 reactor
// (GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared). The host runs
// _initialize, which runs package inits but never main.main, so a Connector
// registers from init().
package connector

import "encoding/json"

// protocolVersion is a var, not a const, so a test fixture can override it
// with -ldflags=-X spindrift.dev/sdk/connector.protocolVersion=0.2.0.
var protocolVersion = "0.1.0"

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
