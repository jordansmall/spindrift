package connectorhost_test

import (
	"context"
	"encoding/json"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
)

// uleb encodes v as unsigned LEB128, which wasm uses for every size and index.
func uleb(v int) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v == 0 {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

func i32Const(v int64) []byte { return append([]byte{0x41}, i64Const(v)[1:]...) }

type guestFn struct {
	name            string
	params, results []byte
	body            []byte
}

type dataSeg struct {
	off int
	b   []byte
}

// guestModule assembles a module of bodies-only exported functions plus one
// page of exported memory, preloaded with data. Each import {module, name} is
// a () -> () function, so the exports' indexes follow them. Unlike wasmModule
// it can place bytes in memory, so a handshake can be served.
func guestModule(imports [][2]string, data []dataSeg, fns ...guestFn) []byte {
	vec := func(b []byte) []byte { return append(uleb(len(b)), b...) }
	section := func(id byte, content []byte) []byte {
		return append(append([]byte{id}, uleb(len(content))...), content...)
	}
	m := []byte("\x00asm\x01\x00\x00\x00")
	types := append(uleb(1+len(fns)), 0x60, 0, 0)
	funcs := uleb(len(fns))
	exports := uleb(len(fns) + 1)
	code := uleb(len(fns))
	for i, f := range fns {
		types = append(append(append(types, 0x60), vec(f.params)...), vec(f.results)...)
		funcs = append(funcs, uleb(i+1)...)
		exports = append(append(append(exports, vec([]byte(f.name))...), 0), uleb(len(imports)+i)...)
		code = append(code, vec(append(append([]byte{0}, f.body...), 0x0b))...)
	}
	exports = append(append(exports, vec([]byte("memory"))...), 2, 0)
	m = append(m, section(1, types)...)
	if len(imports) > 0 {
		imps := uleb(len(imports))
		for _, imp := range imports {
			imps = append(append(imps, vec([]byte(imp[0]))...), vec([]byte(imp[1]))...)
			imps = append(imps, 0, 0)
		}
		m = append(m, section(2, imps)...)
	}
	m = append(m, section(3, funcs)...)
	m = append(m, section(5, []byte{1, 0, 1})...)
	m = append(m, section(7, exports)...)
	m = append(m, section(10, code)...)
	segs := uleb(len(data))
	for _, d := range data {
		segs = append(append(append(segs, 0), i32Const(int64(d.off))...), 0x0b)
		segs = append(segs, vec(d.b)...)
	}
	return append(m, section(11, segs)...)
}

const (
	respOff  = 4096
	allocOff = 8192
	page     = 0x10000
)

// connectorFns are the three exports a call-capable Connector has. The
// handshake serves caps from offset 0, connector_alloc always returns alloc,
// and connector_call always returns ret, a packed ptr<<32|len.
func connectorFns(caps []string, alloc, ret int64) ([]byte, []guestFn) {
	hs, _ := json.Marshal(map[string]any{"protocolVersion": connectorhost.ProtocolVersion, "capabilities": caps})
	return hs, []guestFn{
		{"connector_handshake", nil, []byte{i64}, i64Const(int64(len(hs)))},
		{"connector_alloc", []byte{i32}, []byte{i32}, i32Const(alloc)},
		{"connector_call", []byte{i32, i32}, []byte{i64}, i64Const(ret)},
	}
}

// answeringGuest is a Connector whose every call returns the fixed JSON resp.
func answeringGuest(caps []string, resp string) []byte {
	hs, fns := connectorFns(caps, allocOff, respOff<<32|int64(len(resp)))
	return guestModule(nil, []dataSeg{{0, hs}, {respOff, []byte(resp)}}, fns...)
}

func loadWasm(t *testing.T, wasm []byte) *connectorhost.Connector {
	t.Helper()
	c, err := connectorhost.Load(context.Background(), wasm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}
