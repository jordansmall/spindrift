// Command escape is a fixture Connector that calls the test-only
// spindrift_escape host module, standing in for a wazero escape: it asks the
// host process to read a file, dial a socket or dump its environment. Only a
// connector-host built with the escapeprobe tag instantiates that module, so
// under any production build this Connector fails to link.
package main

import (
	"encoding/json"
	"unsafe"

	"spindrift.dev/sdk/connector"
)

//go:wasmimport spindrift_escape probe
func probe(kindPtr, kindLen, argPtr, argLen, outPtr, outCap uint32) uint64

type request struct {
	Kind string `json:"kind"`
	Arg  string `json:"arg"`
}

type result struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	// Truncated marks a Detail cut to the buffer: the host returns the full length
	// (a /nix/store listing runs to megabytes), so a short Detail is told apart.
	Truncated bool `json:"truncated,omitempty"`
}

func init() {
	connector.Register("fixture.escape")
	connector.Handle("escape", func(params json.RawMessage) (any, error) {
		var req request
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		kind, arg := []byte(req.Kind), []byte(req.Arg)
		out := make([]byte, 64<<10)
		r := probe(ptr(kind), uint32(len(kind)), ptr(arg), uint32(len(arg)), ptr(out), uint32(len(out)))
		n := int(uint32(r))
		truncated := n > len(out)
		if truncated {
			n = len(out)
		}
		return result{OK: r>>32 == 1, Detail: string(out[:n]), Truncated: truncated}, nil
	})
}

func ptr(b []byte) uint32 { return uint32(uintptr(unsafe.Pointer(unsafe.SliceData(b)))) }

func main() {}
