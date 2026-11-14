package connectorhost

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// The Launcher and the out-of-process connector host (Serve) speak length-
// prefixed frames over the host's stdin and stdout: a 4-byte big-endian length
// n, 1 <= n <= maxFrame, then n bytes made of one kind byte and the payload.
// Byte blobs ride raw, so a module is not base64'd.

// maxFrame is the largest frame, the module's: the Launcher refuses to send
// one that does not fit, and a reader checks a length against it before it
// allocates.
const maxFrame = memoryCapBytes

// loadOverhead is the load frame bytes besides the module: the kind byte and
// the grant byte.
const loadOverhead = 2

// Frame kinds. Launcher to host: load, handshake, call, reply, replyFatal;
// host to Launcher: result, failure, httpRequest, refuse.
const (
	kindLoad        byte = iota + 1 // grant byte, wasm
	kindHandshake                   // empty
	kindCall                        // request
	kindReply                       // status u32, body length u32, body, header as JSON
	kindReplyFatal                  // empty: the Launcher's handler failed closed and audited
	kindResult                      // output bytes, empty for a load
	kindFailure                     // code byte, message
	kindHTTPRequest                 // response capacity u32, raw request
	kindRefuse                      // call length u32, call, why
)

// errRemoteHostCall is what the host's hostCalls return once a Host call has
// failed closed in the Launcher, which holds the error and the audit entry.
var errRemoteHostCall = errors.New("host call failed closed in the launcher")

// failureSentinels are the failure codes of a kindFailure frame, by index: the
// sentinel an error maps to the first match of. Append only; code 0 is an error
// that matches none, such as invalid wasm.
var failureSentinels = []error{
	nil, ErrBudget, ErrTrap, ErrMemoryCap, ErrDeclaredSize, ErrOutputTooLarge, ErrMalformedOutput, ErrExport, errRemoteHostCall,
}

// writeFrame writes one frame whose payload is parts joined.
func writeFrame(w io.Writer, kind byte, parts ...[]byte) error {
	n := 1
	for _, p := range parts {
		n += len(p)
	}
	if n > maxFrame {
		return fmt.Errorf("frame of %d bytes does not fit the %d byte cap", n, maxFrame)
	}
	hdr := binary.BigEndian.AppendUint32(nil, uint32(n))
	for _, p := range append([][]byte{append(hdr, kind)}, parts...) {
		// An empty write blocks an io.Pipe until the next read.
		if len(p) == 0 {
			continue
		}
		if _, err := w.Write(p); err != nil {
			return err
		}
	}
	return nil
}

// readFrame reads one frame. io.EOF means the stream ended between frames; a
// stream that ends inside one is io.ErrUnexpectedEOF.
func readFrame(r io.Reader) (kind byte, payload []byte, err error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	// Before the allocation: the length is the peer's to choose.
	if n < 1 || n > maxFrame {
		return 0, nil, fmt.Errorf("%w: frame length %d outside 1..%d", ErrMalformedOutput, n, maxFrame)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	return buf[0], buf[1:], nil
}

func u32(v int) []byte { return binary.BigEndian.AppendUint32(nil, uint32(v)) }

// cutU32 splits a leading u32 off p.
func cutU32(p []byte, what string) (uint32, []byte, error) {
	if len(p) < 4 {
		return 0, nil, fmt.Errorf("%w: %s frame too short", ErrMalformedOutput, what)
	}
	return binary.BigEndian.Uint32(p), p[4:], nil
}

// cutBytes splits a u32-prefixed byte string off p.
func cutBytes(p []byte, what string) (b, rest []byte, err error) {
	n, p, err := cutU32(p, what)
	if err != nil {
		return nil, nil, err
	}
	if uint64(n) > uint64(len(p)) {
		return nil, nil, fmt.Errorf("%w: %s frame too short", ErrMalformedOutput, what)
	}
	return p[:n], p[n:], nil
}

func replyParts(status int, body []byte, header http.Header) [][]byte {
	h, _ := json.Marshal(header) // a map of string slices always marshals
	return [][]byte{u32(status), u32(len(body)), body, h}
}

func decodeReply(p []byte) (status int, body []byte, header http.Header, err error) {
	s, p, err := cutU32(p, "reply")
	if err != nil {
		return 0, nil, nil, err
	}
	body, p, err = cutBytes(p, "reply")
	if err != nil {
		return 0, nil, nil, err
	}
	if err := json.Unmarshal(p, &header); err != nil {
		return 0, nil, nil, fmt.Errorf("%w: reply header: %w", ErrMalformedOutput, err)
	}
	return int(s), body, header, nil
}

func decodeHTTPRequest(p []byte) (bufCap int, raw []byte, err error) {
	c, raw, err := cutU32(p, "httpRequest")
	return int(c), raw, err
}

func decodeRefuse(p []byte) (call, why string, err error) {
	c, w, err := cutBytes(p, "refuse")
	return string(c), string(w), err
}

// failureParts is the kindFailure payload for err: the code of the first
// sentinel it matches, then its message.
func failureParts(err error) [][]byte {
	code := byte(0)
	for i, s := range failureSentinels {
		if s != nil && errors.Is(err, s) {
			code = byte(i)
			break
		}
	}
	return [][]byte{{code}, []byte(err.Error())}
}

// remoteError is a failure the host reported: its message, matching the one
// sentinel the host mapped it to.
type remoteError struct {
	msg      string
	sentinel error
}

func (e *remoteError) Error() string { return e.msg }
func (e *remoteError) Unwrap() error { return e.sentinel }

// decodeFailure rebuilds the error of a kindFailure payload; an unknown code is
// ErrMalformedOutput.
func decodeFailure(p []byte) error {
	if len(p) < 1 {
		return fmt.Errorf("%w: failure frame too short", ErrMalformedOutput)
	}
	msg := string(p[1:])
	if int(p[0]) >= len(failureSentinels) {
		return &remoteError{fmt.Sprintf("%s (unknown failure code %d)", msg, p[0]), ErrMalformedOutput}
	}
	return &remoteError{msg, failureSentinels[p[0]]}
}
