package driverkit

import "bytes"

// LineFramer buffers a stream of arbitrarily-chunked bytes and emits each
// complete newline-terminated line as it becomes available, retaining any
// trailing partial line across calls to Push. It is not safe for concurrent
// use — callers serialize their own calls (e.g. under the Writer's own
// mutex).
type LineFramer struct {
	buf []byte
}

// Push appends p to the framer's buffer, then repeatedly extracts and emits
// (via emit) each complete line found in the buffer, newline stripped. Any
// trailing partial line remains buffered for the next Push.
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
