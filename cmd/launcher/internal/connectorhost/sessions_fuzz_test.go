package connectorhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
)

// recordingCalls is a Host-call handler that only counts what reaches it.
type recordingCalls struct{ n atomic.Int32 }

var _ hostCalls = (*recordingCalls)(nil)

func (r *recordingCalls) httpRequest(context.Context, []byte, int) (int, []byte, http.Header, error) {
	r.n.Add(1)
	return 0, []byte("x"), http.Header{}, nil
}

func (r *recordingCalls) refuse(call, why string) error {
	r.n.Add(1)
	return fmt.Errorf("%w: %s: %s", ErrMalformedOutput, call, why)
}

// sentWriter closes sent on its first Write.
type sentWriter struct {
	io.WriteCloser
	once sync.Once
	sent chan struct{}
}

func (w *sentWriter) Write(p []byte) (int, error) {
	defer w.once.Do(func() { close(w.sent) })
	return w.WriteCloser.Write(p)
}

// probeSession runs a call over ss with a recording Host-call handler, so a
// frame of the host's that reaches the session is served as the engine serves
// it; done closes once the call ends, which it does when the session does. It
// returns once the call is on the wire, so a host that then fails the stream
// cannot beat the call to its first write.
func probeSession(ss *session) (rec *recordingCalls, done <-chan struct{}) {
	rec = &recordingCalls{}
	w := &sentWriter{WriteCloser: ss, sent: make(chan struct{})}
	e := &remoteEngine{link: hostLink{r: ss, w: w, kill: ss.abort, wait: ss.await}, host: rec}
	d := make(chan struct{})
	go func() {
		defer close(d)
		_, _ = e.call(context.Background(), []byte(`{"method":"m"}`))
	}()
	<-w.sent
	return rec, d
}

func httpRequestChunk(id uint32) []byte {
	var req bytes.Buffer
	_ = writeFrame(&req, kindHTTPRequest, u32(64), []byte(`{}`))
	return chunkOf(id, req.Bytes())
}

// FuzzSessionsDemux feeds arbitrary bytes to the Launcher as a hostile host's
// stream after a valid hello, with two sessions open, each serving its own
// Host-call handler: the demux must end, fail the host and leave no session able
// to block a read or a wait. A chunk for a session the pipe never issued must
// fail it, no byte of it may reach a session of another pipe, and a handler
// only ever serves a frame of a chunk naming its own session. Seeds are whole
// chunks, truncated and over-cap ones, http_request frames for sessions of
// either kind, and chunks for sessions never opened or already ended.
func FuzzSessionsDemux(f *testing.F) {
	chunk := func(id uint32, body string) []byte {
		return chunkOf(id, []byte(body))
	}
	f.Add(chunk(1, "abc"))
	f.Add(append(chunk(1, "abc"), chunk(1, "")...))
	f.Add(append(chunk(2, "abc"), chunk(9, "x")...))
	f.Add(append(chunk(1, ""), chunk(1, "late")...))
	f.Add(chunk(3, "x"))
	f.Add(chunk(0, "hello again"))
	f.Add(chunk(1, "abc")[:6])
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 1})
	f.Add(httpRequestChunk(1))
	f.Add(httpRequestChunk(2))
	f.Add(append(httpRequestChunk(2), httpRequestChunk(1)...))
	f.Add(httpRequestChunk(3))
	f.Add(append(httpRequestChunk(1), httpRequestChunk(3)...))
	f.Add(append(chunk(1, ""), httpRequestChunk(1)...))
	f.Fuzz(func(t *testing.T, data []byte) {
		var kills atomic.Int32
		s := newSessions(nopWriteCloser{io.Discard}, func() { kills.Add(1) }, func() error { return nil })
		// The ids the pipe issued, as the test saw them handed out.
		issued := map[uint32]bool{}
		var recs [2]*recordingCalls
		var dones [2]<-chan struct{}
		var owner [2]uint32
		for i := range recs {
			ss, err := s.newSession()
			if err != nil {
				t.Fatal(err)
			}
			issued[ss.id] = true
			owner[i] = ss.id
			recs[i], dones[i] = probeSession(ss)
		}
		// Another pipe's session, whose host stays quiet.
		var otherKills atomic.Int32
		other := newSessions(nopWriteCloser{io.Discard}, func() { otherKills.Add(1) }, func() error { return nil })
		foreign, err := other.newSession()
		if err != nil {
			t.Fatal(err)
		}
		foreignRec, foreignDone := probeSession(foreign)
		quietR, quietW := io.Pipe()
		otherDone := make(chan struct{})
		go func() {
			defer close(otherDone)
			other.fail(other.demux(io.MultiReader(bytes.NewReader(chunkOf(0, nil)), quietR), make(chan struct{})))
		}()

		// The first chunk a pipe cannot serve decides how its demux ends; the
		// ones before it are the only chunks any session is handed.
		var wantRefusal bool
		named := map[uint32]bool{}
		for r := bytes.NewReader(data); ; {
			id, _, err := readChunk(r)
			if err != nil {
				break
			}
			if !issued[id] {
				wantRefusal = true
				break
			}
			named[id] = true
		}
		r := io.MultiReader(bytes.NewReader(chunkOf(0, nil)), bytes.NewReader(data))
		err = s.demux(r, make(chan struct{}))
		if wantRefusal && !errors.Is(err, ErrMalformedOutput) {
			t.Fatalf("demux = %v, want ErrMalformedOutput for a chunk the pipe never issued, for %q", err, data)
		}
		s.fail(err)
		if kills.Load() == 0 {
			t.Fatalf("host not killed after its stream ended, for %q", data)
		}
		for i, d := range dones {
			// Ended by the host or failed: either way the call ends.
			<-d
			if recs[i].n.Load() != 0 && !named[owner[i]] {
				t.Fatalf("session %d served a Host call though no chunk named it, for %q", owner[i], data)
			}
		}

		foreign.mu.Lock()
		reached := len(foreign.buf) != 0 || foreign.hostEOF || foreign.err != nil
		foreign.mu.Unlock()
		if reached || foreignRec.n.Load() != 0 || otherKills.Load() != 0 {
			t.Fatalf("a frame crossed to another pipe's session, for %q", data)
		}
		_ = quietW.Close()
		<-otherDone
		<-foreignDone
	})
}

// A frame in a chunk naming a session reaches that session's Host-call handler
// and no other's, so the fuzz target's property is not vacuous.
func TestSessionsDemuxServesAHostCallOnlyOverItsOwnSession(t *testing.T) {
	s := newSessions(nopWriteCloser{io.Discard}, func() {}, func() error { return nil })
	var recs [2]*recordingCalls
	var dones [2]<-chan struct{}
	for i := range recs {
		ss, err := s.newSession()
		if err != nil {
			t.Fatal(err)
		}
		recs[i], dones[i] = probeSession(ss)
	}
	r := io.MultiReader(bytes.NewReader(chunkOf(0, nil)), bytes.NewReader(httpRequestChunk(2)))
	s.fail(s.demux(r, make(chan struct{})))
	<-dones[0]
	<-dones[1]
	if got := recs[1].n.Load(); got != 1 {
		t.Errorf("session 2's handler served %d calls, want 1", got)
	}
	if got := recs[0].n.Load(); got != 0 {
		t.Errorf("session 1's handler served %d calls for a chunk naming session 2", got)
	}
}

func chunkOf(id uint32, body []byte) []byte {
	var b bytes.Buffer
	_ = writeChunk(&b, id, body)
	return b.Bytes()
}
