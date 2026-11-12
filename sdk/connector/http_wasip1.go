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
