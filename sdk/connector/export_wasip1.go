//go:build wasip1

package connector

import "unsafe"

// result keeps the last returned buffer reachable so the GC cannot free it
// before the host has read it out of linear memory.
var result []byte

//go:wasmexport connector_handshake
func connectorHandshake() uint64 {
	b, err := handshakeJSON()
	if err != nil {
		panic(err) // a fixed struct of strings cannot fail to marshal
	}
	result = b
	return pack(uint32(uintptr(unsafe.Pointer(unsafe.SliceData(b)))), uint32(len(b)))
}
