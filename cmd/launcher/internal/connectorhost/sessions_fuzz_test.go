package connectorhost

import (
	"bytes"
	"io"
	"sync/atomic"
	"testing"
)

// FuzzSessionsDemux feeds arbitrary bytes to the Launcher as a hostile host's
// stream after a valid hello, with two sessions open: the demux must end, fail
// the host and leave no session able to block a read or a wait. Seeds are
// whole chunks, truncated and over-cap ones, and chunks for unknown sessions.
func FuzzSessionsDemux(f *testing.F) {
	chunk := func(id uint32, body string) []byte {
		var b bytes.Buffer
		_ = writeChunk(&b, id, []byte(body))
		return b.Bytes()
	}
	f.Add(chunk(1, "abc"))
	f.Add(append(chunk(1, "abc"), chunk(1, "")...))
	f.Add(append(chunk(2, "abc"), chunk(9, "x")...))
	f.Add(chunk(0, "hello again"))
	f.Add(chunk(1, "abc")[:6])
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		var kills atomic.Int32
		s := newSessions(nopWriteCloser{io.Discard}, func() { kills.Add(1) }, func() error { return nil })
		var open [2]*session
		for i := range open {
			ss, err := s.newSession()
			if err != nil {
				t.Fatal(err)
			}
			open[i] = ss
		}
		r := io.MultiReader(bytes.NewReader(chunkOf(0, nil)), bytes.NewReader(data))
		s.fail(s.demux(r, make(chan struct{})))
		if kills.Load() == 0 {
			t.Fatalf("host not killed after its stream ended, for %q", data)
		}
		for _, ss := range open {
			// Ended by the host or failed: either way a read ends.
			_, _ = io.Copy(io.Discard, ss)
			_ = ss.await()
		}
	})
}

func chunkOf(id uint32, body []byte) []byte {
	var b bytes.Buffer
	_ = writeChunk(&b, id, body)
	return b.Bytes()
}
