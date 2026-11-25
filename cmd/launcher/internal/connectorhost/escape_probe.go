//go:build escapeprobe

package connectorhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
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

// frameRespCap is the response capacity the forged http_request frame claims.
const frameRespCap = 1 << 10

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
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
			stack[0] = escapeProbe(ctx, mod.Memory(), api.DecodeU32(stack[0]), api.DecodeU32(stack[1]), api.DecodeU32(stack[2]),
				api.DecodeU32(stack[3]), api.DecodeU32(stack[4]), api.DecodeU32(stack[5]))
		}), []api.ValueType{i32, i32, i32, i32, i32, i32}, []api.ValueType{i64}).
		Export("probe").Instantiate(ctx)
	return err
}

func escapeProbe(ctx context.Context, mem api.Memory, kindPtr, kindLen, argPtr, argLen, outPtr, outCap uint32) uint64 {
	kind, okK := mem.Read(kindPtr, kindLen)
	arg, okA := mem.Read(argPtr, argLen)
	var ok bool
	var detail string
	switch {
	case !okK || !okA:
		detail = "pointer out of range"
	default:
		var err error
		detail, err = runEscape(ctx, string(kind), string(arg))
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

func runEscape(ctx context.Context, kind, arg string) (string, error) {
	switch kind {
	case "read":
		return escapeRead(arg)
	case "write":
		return escapeWrite(arg)
	case "tcp", "unix":
		c, err := net.DialTimeout(kind, arg, dialTimeout)
		if err != nil {
			return "", err
		}
		return "connected", c.Close()
	case "frame":
		return escapeFrame(ctx, arg)
	case "env":
		return strings.Join(os.Environ(), "\n"), nil
	}
	return "", fmt.Errorf("unknown escape kind %q", kind)
}

// escapeWrite creates path exclusively and removes it again, so a write that
// succeeds leaves nothing behind.
func escapeWrite(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	_, werr := f.Write([]byte{0})
	cerr := f.Close()
	if err := os.Remove(path); err != nil {
		return "", err
	}
	if werr != nil {
		return "", werr
	}
	return "written", cerr
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

type escapeOutKey struct{}

// withEscapeOut hands the "frame" probe the host's chunk writer, so a forged
// chunk is serialized with the sessions' own and cannot split one.
func withEscapeOut(ctx context.Context, out *chunkWriter) context.Context {
	return context.WithValue(ctx, escapeOutKey{}, out)
}

// escapeFrame writes a well-formed http_request frame (GET of a URL) onto the
// host's stream as a chunk of session id: what a compromised host could send
// to speak as a Connector that is not its own. arg is "<id> <url>".
func escapeFrame(ctx context.Context, arg string) (string, error) {
	out, _ := ctx.Value(escapeOutKey{}).(*chunkWriter)
	if out == nil {
		return "", errors.New("frame: not served by ServeSessions")
	}
	idStr, url, ok := strings.Cut(arg, " ")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if !ok || err != nil {
		return "", fmt.Errorf("frame: arg %q is not \"<id> <url>\"", arg)
	}
	raw, err := json.Marshal(httpRequest{Method: "GET", URL: url})
	if err != nil {
		return "", err
	}
	var frame bytes.Buffer
	if err := writeFrame(&frame, kindHTTPRequest, u32(frameRespCap), raw); err != nil {
		return "", err
	}
	if err := out.send(uint32(id), frame.Bytes()); err != nil {
		return "", err
	}
	return "sent", nil
}
