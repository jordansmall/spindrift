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

// input is the buffer connector_alloc last handed the host. connector_call
// reads it back from here rather than rebuilding a pointer from the uintptr
// the host passes, which keeps the GC's view of the buffer sound.
var input []byte

//go:wasmexport connector_alloc
func connectorAlloc(size uint32) uint32 {
	input = make([]byte, size)
	return uint32(uintptr(unsafe.Pointer(unsafe.SliceData(input))))
}

//go:wasmexport connector_call
func connectorCall(ptr, length uint32) uint64 {
	if uint32(uintptr(unsafe.Pointer(unsafe.SliceData(input)))) != ptr || int(length) > cap(input) {
		result = errorResponse(Permanent, "connector_call does not match the last connector_alloc")
	} else {
		result = call(input[:length])
	}
	return pack(uint32(uintptr(unsafe.Pointer(unsafe.SliceData(result)))), uint32(len(result)))
}
