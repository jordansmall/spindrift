//go:build escapeprobe

package connectorhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// escapeModule names the test-only host module standing in for a wazero
// escape: its functions run in the connector-host process, so what they can
// reach is exactly what the host's own sandbox leaves reachable. This file is
// compiled only under the escapeprobe tag, which no production binary or the
// release image sets.
const escapeModule = "spindrift_escape"

// readPrefix bounds the file bytes a "read" probe returns.
const readPrefix = 256

// dialTimeout bounds each "tcp" and "unix" probe dial.
const dialTimeout = 2 * time.Second

// instantiateEscape exports probe(kindPtr, kindLen, argPtr, argLen, outPtr,
// outCap i32) -> i64. The result is ok<<32 | n: ok is 1 on success and 0 on
// failure, and min(n, outCap) bytes of detail (the data, or the error text)
// are written at outPtr. A bad pointer or unknown kind is a failure, never a
// panic.
func instantiateEscape(ctx context.Context, rt wazero.Runtime) error {
	i32, i64 := api.ValueTypeI32, api.ValueTypeI64
	_, err := rt.NewHostModuleBuilder(escapeModule).NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, mod api.Module, stack []uint64) {
			stack[0] = escapeProbe(mod.Memory(), api.DecodeU32(stack[0]), api.DecodeU32(stack[1]), api.DecodeU32(stack[2]),
				api.DecodeU32(stack[3]), api.DecodeU32(stack[4]), api.DecodeU32(stack[5]))
		}), []api.ValueType{i32, i32, i32, i32, i32, i32}, []api.ValueType{i64}).
		Export("probe").Instantiate(ctx)
	return err
}

func escapeProbe(mem api.Memory, kindPtr, kindLen, argPtr, argLen, outPtr, outCap uint32) uint64 {
	kind, okK := mem.Read(kindPtr, kindLen)
	arg, okA := mem.Read(argPtr, argLen)
	var ok bool
	var detail string
	switch {
	case !okK || !okA:
		detail = "pointer out of range"
	default:
		var err error
		detail, err = runEscape(string(kind), string(arg))
		ok = err == nil
		if err != nil {
			detail = err.Error()
		}
	}
	out := []byte(detail)
	n := uint64(len(out))
	if len(out) > int(outCap) {
		out = out[:outCap]
	}
	if !mem.Write(outPtr, out) {
		ok, n = false, 0
	}
	var okBit uint64
	if ok {
		okBit = 1
	}
	return okBit<<32 | n
}

func runEscape(kind, arg string) (string, error) {
	switch kind {
	case "read":
		return escapeRead(arg)
	case "tcp", "unix":
		c, err := net.DialTimeout(kind, arg, dialTimeout)
		if err != nil {
			return "", err
		}
		return "connected", c.Close()
	case "env":
		return strings.Join(os.Environ(), "\n"), nil
	}
	return "", fmt.Errorf("unknown escape kind %q", kind)
}

func escapeRead(path string) (string, error) {
	if entries, err := os.ReadDir(path); err == nil {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		return strings.Join(names, "\n"), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, readPrefix)
	n, err := f.Read(buf)
	if err != nil && n == 0 && !errors.Is(err, io.EOF) {
		return "", err
	}
	return string(buf[:n]), nil
}
