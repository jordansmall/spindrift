package connectorhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"spindrift.dev/launcher/internal/panicguard"
)

// sessionBufCap bounds the bytes of one session the host has sent that its
// engine has not read yet: a whole frame of the largest size and room over. The
// host is untrusted, so a session past it fails the host rather than growing.
var sessionBufCap = maxFrame + 1<<16

// Sessions is the Launcher's end of one connector host serving many Connectors
// over a single stream (see wire.go): each Load opens a session of its own.
type Sessions struct {
	w    io.WriteCloser
	kill func()
	wait func() error

	// wmu serializes chunks onto w.
	wmu sync.Mutex

	mu       sync.Mutex
	open     map[uint32]*session
	last     uint32
	dead     error
	closed   bool
	closing  atomic.Bool
	killed   atomic.Bool
	readDone chan struct{}

	meter hopMeter
}

var errSessionKilled = errors.New("session ended by the launcher")

// NewSessions waits for the hello of a host whose stdout is r and stdin is w,
// and returns the Sessions over it. kill stops the host without waiting, and
// wait returns once it has exited. If ctx ends or the stream ends before the
// hello, the host is killed and waited for, and the error says why.
func NewSessions(ctx context.Context, r io.Reader, w io.WriteCloser, kill func(), wait func() error) (*Sessions, error) {
	s := newSessions(w, kill, wait)
	hello := make(chan struct{})
	panicguard.Go(func() {
		defer close(s.readDone)
		s.fail(s.demux(r, hello))
	})
	select {
	case <-hello:
		return s, nil
	case <-s.readDone:
		s.mu.Lock()
		err := s.dead
		s.mu.Unlock()
		err = fmt.Errorf("connector host sent no hello: %w", err)
		s.stop()
		return nil, err
	case <-ctx.Done():
		s.stop()
		return nil, fmt.Errorf("connector host sent no hello: %w", context.Cause(ctx))
	}
}

func newSessions(w io.WriteCloser, kill func(), wait func() error) *Sessions {
	return &Sessions{w: w, kill: kill, wait: wait, open: map[uint32]*session{}, readDone: make(chan struct{})}
}

// stop kills the host and reaps it once nothing reads its stream.
func (s *Sessions) stop() {
	s.killHost()
	<-s.readDone
	_ = s.wait()
}

func (s *Sessions) killHost() {
	s.killed.Store(true)
	s.kill()
}

// demux reads the host's chunks until the stream ends or breaks, handing each to
// its session. It never blocks on a session. The first chunk is the hello.
func (s *Sessions) demux(r io.Reader, hello chan<- struct{}) error {
	id, body, err := readChunk(r)
	if err != nil {
		return streamError(err)
	}
	if id != 0 || len(body) != 0 {
		return fmt.Errorf("%w: first chunk is not the hello", ErrMalformedOutput)
	}
	close(hello)
	for {
		id, body, err := readChunk(r)
		if err != nil {
			return streamError(err)
		}
		if id == 0 {
			return fmt.Errorf("%w: chunk for session 0 after the hello", ErrMalformedOutput)
		}
		if err := s.deliver(id, body); err != nil {
			return err
		}
	}
}

func streamError(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// deliver hands body to session id, which a host may have ended or never been
// given, in which case it is dropped. An error is the host's to answer for.
func (s *Sessions) deliver(id uint32, body []byte) error {
	s.mu.Lock()
	ss := s.open[id]
	if ss == nil {
		s.mu.Unlock()
		return nil
	}
	if len(body) == 0 {
		delete(s.open, id)
	}
	s.mu.Unlock()
	return ss.receive(body)
}

// fail ends every open session with err and kills the host, since the stream
// cannot be trusted past it. A host that ends its stream while Close is waiting
// for it is left to exit by itself, so that its status is Close's.
func (s *Sessions) fail(err error) {
	s.mu.Lock()
	if s.dead == nil {
		s.dead = err
	}
	open := s.open
	s.open = map[uint32]*session{}
	s.mu.Unlock()
	for _, ss := range open {
		ss.fail(err)
	}
	if !s.closing.Load() {
		s.killHost()
	}
}

func (s *Sessions) send(id uint32, body []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return writeChunk(s.w, id, body)
}

// Load is Load over a new session of the host.
func (s *Sessions) Load(ctx context.Context, wasm []byte) (*Connector, error) {
	return loadWith(ctx, wasm, s.engine, nil)
}

// LoadNetwork is LoadNetwork over a new session of the host: each http_request
// crosses back to the Launcher as LoadNetworkRemote's does.
func (s *Sessions) LoadNetwork(ctx context.Context, wasm []byte, n Network) (*Connector, error) {
	if err := checkGrant(n); err != nil {
		return nil, err
	}
	return loadWith(ctx, wasm, s.engine, &n)
}

// engine is the engineFactory over a new session.
func (s *Sessions) engine(ctx context.Context, wasm []byte, host hostCalls) (engine, error) {
	ss, err := s.newSession()
	if err != nil {
		return nil, err
	}
	return newRemoteEngine(ctx, wasm, host, hostLink{r: ss, w: ss, kill: ss.abort, wait: ss.await, meter: &s.meter})
}

// Stats is what the exchanges over every session of the host have cost so far;
// safe concurrently and after Close.
func (s *Sessions) Stats() HopStats { return s.meter.stats() }

func (s *Sessions) newSession() (*session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead != nil {
		return nil, fmt.Errorf("connector host exited: %w", s.dead)
	}
	if s.closed {
		return nil, errors.New("connector host closed")
	}
	s.last++
	ss := &session{s: s, id: s.last}
	ss.cond = sync.NewCond(&ss.mu)
	s.open[ss.id] = ss
	return ss, nil
}

// Close ends the host: its stdin closes, which it takes as the end of every
// session, and it is killed if it has not exited within closeGrace or ctx ends.
func (s *Sessions) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.closing.Store(true)
	_ = s.w.Close()
	graceKilled, ctxKilled := make(chan struct{}), make(chan struct{})
	timer := time.AfterFunc(closeGrace, func() { defer close(graceKilled); s.killHost() })
	stop := context.AfterFunc(ctx, func() { defer close(ctxKilled); s.killHost() })
	// A killer that already fired runs on its own goroutine and may still be
	// mid-kill once the host is reaped: wait for it, so no kill outlives Close.
	defer func() {
		if !timer.Stop() {
			<-graceKilled
		}
		if !stop() {
			<-ctxKilled
		}
	}()
	// The host's stdout ends when it exits, and the reads finish before it is
	// reaped.
	<-s.readDone
	err := s.wait()
	if s.killed.Load() {
		return nil
	}
	return err
}

// session is one Connector's byte stream to the host: the Launcher's engine
// reads and writes it as it would a process's pipes.
type session struct {
	s  *Sessions
	id uint32

	// eof is the Launcher's empty chunk, sent once.
	eof sync.Once

	mu   sync.Mutex
	cond *sync.Cond
	buf  []byte
	// hostEOF is the host's empty chunk: it has sent all it will.
	hostEOF bool
	// err ends the session early: killed, or the host failed.
	err error
	// sent is set once the Launcher has ended its direction.
	sent bool
}

var _ io.ReadWriteCloser = (*session)(nil)

// receive adds the host's chunk to the buffer, or ends the host's direction on an
// empty one. A buffer past its bound is an error: the host is not reading it.
func (ss *session) receive(body []byte) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.hostEOF || ss.err != nil {
		return nil
	}
	if len(body) == 0 {
		ss.hostEOF = true
		ss.cond.Broadcast()
		return nil
	}
	if len(ss.buf)+len(body) > sessionBufCap {
		return fmt.Errorf("session %d: host sent more than %d unread bytes", ss.id, sessionBufCap)
	}
	ss.buf = append(ss.buf, body...)
	ss.cond.Broadcast()
	return nil
}

// fail ends the session because the host failed; what it sent is still read.
func (ss *session) fail(err error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.err == nil && !ss.hostEOF {
		ss.err = err
	}
	ss.cond.Broadcast()
}

func (ss *session) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	for len(ss.buf) == 0 && !ss.hostEOF && ss.err == nil {
		ss.cond.Wait()
	}
	if len(ss.buf) > 0 {
		n := copy(p, ss.buf)
		ss.buf = ss.buf[n:]
		return n, nil
	}
	if ss.err != nil {
		return 0, ss.err
	}
	return 0, io.EOF
}

func (ss *session) Write(p []byte) (int, error) {
	ss.mu.Lock()
	err, sent := ss.err, ss.sent
	ss.mu.Unlock()
	if err != nil {
		return 0, err
	}
	if sent {
		return 0, io.ErrClosedPipe
	}
	// An empty chunk would end the session.
	if len(p) == 0 {
		return 0, nil
	}
	if err := ss.s.send(ss.id, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close ends the Launcher's direction: the host takes it as the end of the
// session.
func (ss *session) Close() (err error) {
	ss.eof.Do(func() {
		ss.mu.Lock()
		ss.sent = true
		ss.mu.Unlock()
		err = ss.s.send(ss.id, nil)
	})
	return err
}

// abort ends this session alone: reads fail at once and the host is told to stop
// it. The host stays up for the other sessions, so no write is waited for.
func (ss *session) abort() {
	ss.mu.Lock()
	if ss.err == nil && !ss.hostEOF {
		ss.err = errSessionKilled
	}
	ss.buf = nil
	ss.cond.Broadcast()
	ss.mu.Unlock()
	ss.s.mu.Lock()
	delete(ss.s.open, ss.id)
	ss.s.mu.Unlock()
	panicguard.Go(func() { _ = ss.Close() })
}

// await blocks until the host has ended the session, or it was aborted or the
// host failed. Its error is the failure, nil for a session that ended.
func (ss *session) await() error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	for !ss.hostEOF && ss.err == nil {
		ss.cond.Wait()
	}
	if ss.hostEOF {
		return nil
	}
	return ss.err
}
