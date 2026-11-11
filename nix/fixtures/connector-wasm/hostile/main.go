// Command hostile is a fixture Connector whose connector_call misbehaves as
// the link-time mode says (-ldflags=-X=main.mode=<mode>; the sandbox has no env
// or args): trap, loop, memory, oversize, malformed (not JSON) or invalid
// (JSON whose string is not UTF-8). Its handshake is valid.
//
// It exports the call ABI itself instead of using sdk/connector: the SDK's
// connector_call only ever answers well-formed JSON, and two packages cannot
// export the same name.
package main

import (
	"unsafe"
)

var mode string

var (
	// sink keeps the memory mode's allocations reachable so the GC cannot drop them.
	sink [][]byte
	// result is what connector_call returns; input is the buffer connector_alloc
	// handed out. Both stay reachable so the GC cannot free them under the host.
	result, input []byte
	// spin is a var so the compiler cannot elide the loop mode's empty loop.
	spin = true
)

func pack(b []byte) uint64 {
	return uint64(uint32(uintptr(unsafe.Pointer(unsafe.SliceData(b)))))<<32 | uint64(uint32(len(b)))
}

// The version is hard-coded: sdk/connector would supply it, but importing it
// links its own call exports.
//
//go:wasmexport connector_handshake
func connectorHandshake() uint64 {
	result = []byte(`{"protocolVersion":"0.1.0","capabilities":["fixture.hostile"]}`)
	return pack(result)
}

//go:wasmexport connector_alloc
func connectorAlloc(size uint32) uint32 {
	input = make([]byte, size)
	return uint32(uintptr(unsafe.Pointer(unsafe.SliceData(input))))
}

//go:wasmexport connector_call
func connectorCall(ptr, length uint32) uint64 {
	switch mode {
	case "trap":
		panic("hostile fixture trap")
	case "loop":
		for spin {
		}
	case "memory":
		for { // far past the host's cap; the host stops it
			b := make([]byte, 1<<20)
			for i := 0; i < len(b); i += 4096 {
				b[i] = 1 // one write per page; a byte loop is too slow under the interpreter
			}
			sink = append(sink, b)
		}
	case "oversize":
		result = make([]byte, 16<<20) // within memory, over the output cap; the host checks the length before reading the bytes
	case "malformed":
		result = []byte("not json")
	case "invalid":
		result = []byte("{\"result\":\"\xff\"}") // json.Valid accepts the byte; the host also requires UTF-8
	default:
		panic("hostile fixture: unknown mode " + mode)
	}
	return pack(result)
}

func main() {}
