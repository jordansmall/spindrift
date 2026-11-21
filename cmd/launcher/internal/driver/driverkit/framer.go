package driverkit

import "bytes"

// LineFramer emits each complete line of an arbitrarily-chunked byte stream,
// holding a trailing partial line across Push calls. It is not safe for
// concurrent use; callers serialize their own calls.
type LineFramer struct {
	buf []byte
}

// Push appends p and emits each complete buffered line, newline stripped.
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

// Drain returns the buffered trailing partial line, newline-less, and empties
// the buffer.
func (f *LineFramer) Drain() string {
	rest := string(f.buf)
	f.buf = nil
	return rest
}
