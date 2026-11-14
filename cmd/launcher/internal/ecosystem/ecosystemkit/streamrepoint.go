package ecosystemkit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"spindrift.dev/launcher/internal/registryvocab"
)

// streamWindowBytes caps the bytes StreamRepointJSON holds unwritten while it
// tokenizes; a token over the window just abandons the rewrite and relays
// verbatim.
const streamWindowBytes = 1 << 20

// streamWriteBufferBytes sizes the buffer between the rewriter and dst.
const streamWriteBufferBytes = 32 << 10

// errStreamWindow is the recorder's "window full" signal to the decoder.
var errStreamWindow = errors.New("token exceeds the stream window")

// JSONPathStep is one open container on the path from a JSON document's root
// to a string value. Key is the member the path descends through when the
// container is an object, and empty for an array.
type JSONPathStep struct {
	IsObject bool
	Key      string
}

// streamRecorder tees every byte the json.Decoder reads into raw, so the
// rewriter can emit the exact source bytes of what the decoder has tokenized.
// raw[0] sits at stream offset base; bytes before written are already in dst.
type streamRecorder struct {
	src     io.Reader
	raw     []byte
	base    int64
	written int64
	srcErr  error // a genuine (non-EOF) src read error
}

func (r *streamRecorder) Read(p []byte) (int, error) {
	if r.srcErr != nil {
		return 0, r.srcErr
	}
	// The decoder only reads once every complete token it holds is consumed,
	// so what is left unwritten is the one token still in flight; dropping the
	// written prefix here is cheap and keeps the per-token path copy-free.
	if drop := r.written - r.base; drop > 0 {
		r.raw = r.raw[:copy(r.raw, r.raw[drop:])]
		r.base = r.written
	}
	room := streamWindowBytes - len(r.raw)
	if room <= 0 {
		return 0, errStreamWindow
	}
	if len(p) > room {
		p = p[:room]
	}
	n, err := r.src.Read(p)
	r.raw = append(r.raw, p[:n]...)
	if err != nil && err != io.EOF {
		r.srcErr = err
	}
	return n, err
}

// StreamRepointJSON copies the JSON document(s) in src to dst, repointing each
// string value selects accepts at the Forwarder via RepointRegistryURL, and
// passes every other byte through verbatim. It holds at most streamWindowBytes
// unwritten. selects receives the container path from the root to the value
// (valid only for the call; object keys are never offered). A foreign-host
// value is a declined edit (empty To), and a selected string that is not an
// absolute URL is left alone without one. Edits reach onEdit (which may be
// nil) in document order, each before the bytes it edited reach dst. A body
// that cannot be interpreted further is relayed byte-identical and reported as
// registryvocab.ErrRewriteAbandoned (see finishStream). Concatenated documents
// are each rewritten; trailing data after the first is not an abandonment.
func StreamRepointJSON(dst io.Writer, src io.Reader, rc registryvocab.RewriteContext, onEdit func(registryvocab.RewriteEdit), selects func(path []JSONPathStep) bool) error {
	rec := &streamRecorder{src: src}
	out := bufio.NewWriterSize(dst, streamWriteBufferBytes)
	dec := json.NewDecoder(rec)
	dec.UseNumber()

	var path []JSONPathStep
	// expectKey: the innermost container is an object awaiting its next key.
	expectKey := false

	for {
		tok, err := dec.Token()
		if err != nil {
			return finishStream(out, rec, src, err, len(path) > 0)
		}
		off := dec.InputOffset()

		// done: the token completed a value of the innermost container, so an
		// object there expects its next key.
		done := true
		target := ""
		isTarget := false

		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{', '[':
				done = false
				path = append(path, JSONPathStep{IsObject: v == '{'})
				expectKey = v == '{'
			default:
				path = path[:len(path)-1]
			}
		case string:
			if expectKey {
				path[len(path)-1].Key = v
				expectKey = false
				done = false
			} else if selects(path) {
				target, isTarget = v, true
			}
		}

		if isTarget {
			err = writeRepointed(out, rec, off, target, rc, onEdit)
		} else {
			err = flushTo(out, rec, off)
		}
		if err != nil {
			return err
		}

		if done {
			expectKey = len(path) > 0 && path[len(path)-1].IsObject
		}
	}
}

// flushTo writes the not-yet-written source bytes up to stream offset off.
func flushTo(out *bufio.Writer, rec *streamRecorder, off int64) error {
	_, err := out.Write(rec.raw[rec.written-rec.base : off-rec.base])
	rec.written = off
	return err
}

// writeRepointed handles a selected string value ending at stream offset off.
// The pending segment is whatever separates it from the previous token (the
// swallowed ':' and whitespace) followed by the quoted string, so the value
// starts at the segment's first '"'.
func writeRepointed(out *bufio.Writer, rec *streamRecorder, off int64, value string, rc registryvocab.RewriteContext, onEdit func(registryvocab.RewriteEdit)) error {
	seg := rec.raw[rec.written-rec.base : off-rec.base]
	edit, ok := RepointRegistryURL(value, rc)
	if !ok {
		return flushTo(out, rec, off)
	}
	// Checked before onEdit so onEdit runs only for a value actually edited or
	// declined. A string token's segment always holds its opening quote, so
	// this is defensive.
	quote := bytes.IndexByte(seg, '"')
	if quote < 0 {
		return flushTo(out, rec, off)
	}
	if onEdit != nil {
		onEdit(edit)
	}
	if edit.To == "" {
		return flushTo(out, rec, off)
	}
	encoded, err := json.Marshal(edit.To)
	if err != nil {
		// Unreachable: a string always marshals.
		return flushTo(out, rec, off)
	}
	if _, err := out.Write(seg[:quote]); err != nil {
		return err
	}
	rec.written = off
	_, err = out.Write(encoded)
	return err
}

// finishStream ends the stream on a Token error. EOF with no container open
// is the normal end (one cut off inside a container is abandoned); a
// src read failure is returned as itself; anything else (a syntax error, a
// token over the window) abandons interpretation: the unwritten bytes and the
// rest of src go through verbatim before the abandonment is reported.
func finishStream(out *bufio.Writer, rec *streamRecorder, src io.Reader, tokErr error, open bool) error {
	if rec.srcErr != nil {
		return rec.srcErr
	}
	_, werr := out.Write(rec.raw[rec.written-rec.base:])
	if tokErr == io.EOF && open {
		// The decoder reports a body cut off inside a container as a bare EOF.
		tokErr = io.ErrUnexpectedEOF
	}
	if tokErr == io.EOF {
		if werr != nil {
			return werr
		}
		return out.Flush()
	}
	if werr == nil {
		_, werr = io.Copy(out, src)
	}
	if werr == nil {
		werr = out.Flush()
	}
	if werr != nil {
		return werr
	}
	return fmt.Errorf("%w: %w", registryvocab.ErrRewriteAbandoned, tokErr)
}
