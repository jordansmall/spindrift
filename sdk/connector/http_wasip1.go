//go:build wasip1

package connector

import (
	"runtime"
	"unsafe"
)

// httpRequestImport is the host's spindrift.http_request:
// (reqPtr, reqLen, respPtr, respCap) -> status<<32 | n.
//
//go:wasmimport spindrift http_request
func httpRequestImport(reqPtr, reqLen, respPtr, respCap uint32) uint64

func hostHTTPRequest(req, resp []byte) (uint64, error) {
	r := httpRequestImport(
		uint32(uintptr(unsafe.Pointer(unsafe.SliceData(req)))), uint32(len(req)),
		uint32(uintptr(unsafe.Pointer(unsafe.SliceData(resp)))), uint32(len(resp)))
	runtime.KeepAlive(req)
	runtime.KeepAlive(resp)
	return r, nil
}

// responseHeaderImport is the host's spindrift.http_response_header:
// (namePtr, nameLen, outPtr, outCap) -> found<<32 | n.
//
//go:wasmimport spindrift http_response_header
func responseHeaderImport(namePtr, nameLen, outPtr, outCap uint32) uint64

func hostResponseHeader(name, out []byte) (uint64, error) {
	r := responseHeaderImport(
		uint32(uintptr(unsafe.Pointer(unsafe.SliceData(name)))), uint32(len(name)),
		uint32(uintptr(unsafe.Pointer(unsafe.SliceData(out)))), uint32(len(out)))
	runtime.KeepAlive(name)
	runtime.KeepAlive(out)
	return r, nil
}
