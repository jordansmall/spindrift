package driver

import "bytes"

// LineFramer buffers a stream of arbitrarily-chunked bytes and emits each
// complete newline-terminated line, keeping a trailing partial line across
// calls to Push. It is the twin of driverkit's LineFramer, pinned by
// TestLineFramerParity. It is not safe for concurrent use.
type LineFramer struct {
	buf []byte
}

// Push appends p, then emits each complete line in the buffer, newline
// stripped.
func (f *LineFramer) Push(p []byte, emit func(line string)) {
	f.buf = append(f.buf, p...)
	for {
		nl := bytes.IndexByte(f.buf, '\n')
		if nl < 0 {
			break
		}
		line := string(f.buf[:nl])
		f.buf = f.buf[nl+1:]
		emit(line)
	}
}

// Drain returns the buffered trailing partial line and empties the buffer.
func (f *LineFramer) Drain() string {
	rest := string(f.buf)
	f.buf = nil
	return rest
}
