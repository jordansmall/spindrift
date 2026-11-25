package connectorhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"spindrift.dev/launcher/internal/panicguard"
)

// ServeSessions is the connector host process: it serves every Connector of a
// run over the one stream r and w, each as a Serve over its own session (see
// wire.go). It says hello on w first, so the Launcher knows the host started,
// then returns once r ends between chunks, after ending every session and
// waiting for its Serve. A malformed chunk is an error.
//
// A session's own failure ends that session alone: the Launcher sees its stream
// end. Only the stream's failure is ServeSessions's.
func ServeSessions(ctx context.Context, r io.Reader, w io.Writer) error {
	out := &chunkWriter{w: w}
	ctx = withEscapeOut(ctx, out)
	if err := out.send(0, nil); err != nil {
		return err
	}
	var (
		wg      sync.WaitGroup
		live    = map[uint32]*hostSession{}
		highest uint32
	)
	defer func() {
		for _, s := range live {
			s.end()
		}
		wg.Wait()
	}()
	for {
		id, body, err := readChunk(r)
		if errors.Is(err, io.EOF) {
			return out.failure()
		}
		if err != nil {
			return err
		}
		if id == 0 {
			return errors.New("chunk for session 0, which is the hello's")
		}
		s := live[id]
		if s == nil {
			// Ids only rise, so a chunk below the highest is for a session already
			// ended, which must not start again.
			if id <= highest {
				continue
			}
			highest = id
			s = startHostSession(ctx, id, out, &wg)
			live[id] = s
		}
		if len(body) == 0 {
			s.end()
			delete(live, id)
			continue
		}
		// Blocks while the session's Serve is not reading. The Launcher sends
		// only what Serve is about to read, and a Serve that has returned has
		// closed its reader, so this fails rather than waiting.
		_, _ = s.in.Write(body)
	}
}

// chunkWriter serializes the sessions' chunks onto the host's stdout.
type chunkWriter struct {
	mu  sync.Mutex
	w   io.Writer
	err error
}

func (c *chunkWriter) send(session uint32, body []byte) error {
	// Refused before it can poison the stream, which this one write did not break.
	if len(body) > maxFrame {
		return fmt.Errorf("chunk of %d bytes does not fit the %d byte cap", len(body), maxFrame)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.err = writeChunk(c.w, session, body)
	return c.err
}

func (c *chunkWriter) failure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// hostSession is one session's Serve and the pipe feeding it.
type hostSession struct {
	in     *io.PipeWriter
	cancel context.CancelFunc
}

// end is the Launcher ending the session: Serve sees its stream end, and a
// call in flight is cancelled, since the Launcher gave up on it.
func (s *hostSession) end() {
	_ = s.in.Close()
	s.cancel()
}

func startHostSession(ctx context.Context, id uint32, out *chunkWriter, wg *sync.WaitGroup) *hostSession {
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(ctx)
	wg.Add(1)
	panicguard.Go(func() {
		defer wg.Done()
		defer cancel()
		// An error is the session's alone and reaches the Launcher as the end of
		// its stream.
		_ = Serve(ctx, pr, sessionWriter{out, id})
		_ = pr.Close()
		_ = out.send(id, nil)
	})
	return &hostSession{in: pw, cancel: cancel}
}

// sessionWriter sends each Write as one chunk of its session.
type sessionWriter struct {
	out *chunkWriter
	id  uint32
}

func (s sessionWriter) Write(p []byte) (int, error) {
	// An empty chunk would end the session.
	if len(p) == 0 {
		return 0, nil
	}
	if err := s.out.send(s.id, p); err != nil {
		return 0, fmt.Errorf("session %d: %w", s.id, err)
	}
	return len(p), nil
}
