package shadow

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/shadowrecord"
)

const (
	// filterChunk is os/exec's copy buffer size, so the filters see the write
	// sizes the live tee gives them.
	filterChunk = 32 << 10
	// regionMax bounds what each side of a streamFilter divergence keeps from the
	// first differing line on; it exceeds shadowrecord's payload clip, so the clip
	// does the trimming.
	regionMax = shadowrecord.MaxPayload + 1
	// prefixMax bounds the matched head of the differing line kept in front of
	// both regions, so a very long line cannot grow it.
	prefixMax = 1 << 10
)

// filterCompare runs the stream-filter check: the raw stdout copy at rawPath
// through the native filter and the Connector's, the latter's output compared
// with the native filter's actual output in logPath. Every returned error lands
// in Sum.FilterError, never Sum.Error: the filter shadow cannot fail the run.
// (A panic inside a filter is no returned error: run.finish recovers it into
// Sum.Error.) A native Strategy without a filter has nothing to compare.
func (r *run) filterCompare(ctx context.Context, shadow driverkit.Strategy, failed func() error, logPath, rawPath string) {
	if r.native.NewStreamFilter == nil {
		return
	}
	if rawPath == "" {
		r.Sum.FilterError = "no raw stdout copy"
		return
	}
	cmp := &regionCmp{line: 1}
	err := r.filterRun(ctx, shadow, failed, logPath, rawPath, cmp)
	if cmp.diverged {
		r.Sum.FilterDiverged = true
		r.Check("streamFilter", cmp.line, "", encodeRegion(cmp.native), encodeRegion(cmp.shadow))
	}
	if err != nil {
		r.Sum.FilterError = err.Error()
		return
	}
	r.Sum.FilterRan = true
}

// invalidUTF8Key names the object a non-UTF-8 region is recorded under.
const invalidUTF8Key = "invalidUTF8"

// encodeRegion encodes a region for Check without loss: a plain JSON string maps
// every invalid UTF-8 byte to U+FFFD, so regions differing only in such bytes
// would compare equal and go unrecorded. An invalid region becomes an object,
// which no string encoding can equal.
func encodeRegion(b []byte) json.RawMessage {
	if utf8.Valid(b) {
		return shadowrecord.EncodeJSON(string(b))
	}
	return shadowrecord.EncodeJSON(map[string]string{invalidUTF8Key: strconv.Quote(string(b))})
}

func (r *run) filterRun(ctx context.Context, shadow driverkit.Strategy, failed func() error, logPath, rawPath string, cmp *regionCmp) error {
	// ctx comes first, as in compare: an aborted Connector fails with a trap that
	// would hide the deadline.
	stop := func() error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("filter shadow deadline exceeded after %d bytes: %w", r.Sum.FilterBytes, err)
		}
		return failed()
	}
	raw, err := os.Open(rawPath)
	if err != nil {
		return err
	}
	defer func() { _ = raw.Close() }()
	var ref io.Reader = bytes.NewReader(nil)
	if f, err := os.Open(logPath); err == nil {
		defer func() { _ = f.Close() }()
		ref = f
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	cmp.ref = bufio.NewReaderSize(ref, filterChunk)

	var out bytes.Buffer
	nativeF, shadowF := newFilter(r.native, io.Discard), newFilter(shadow, &out)
	// step times one filter call and reports its error, a deadline first.
	step := func(who string, spent *time.Duration, call func() error) error {
		t := time.Now()
		err := call()
		*spent += time.Since(t)
		if err != nil {
			if e := stop(); e != nil {
				return e
			}
			return fmt.Errorf("%s filter: %w", who, err)
		}
		return nil
	}
	feed := func() error {
		defer out.Reset()
		return cmp.feed(out.Bytes())
	}
	buf := make([]byte, filterChunk)
	for {
		if err := stop(); err != nil {
			return err
		}
		n, rerr := raw.Read(buf)
		if n > 0 {
			p := buf[:n]
			if err := step("native", &r.filterNative, func() error { _, err := nativeF.Write(p); return err }); err != nil {
				return err
			}
			if err := step("connector", &r.filterShadow, func() error { _, err := shadowF.Write(p); return err }); err != nil {
				return err
			}
			r.Sum.FilterBytes += int64(n)
			if err := feed(); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("read %s: %w", rawPath, rerr)
		}
	}
	if err := step("native", &r.filterNative, nativeF.Flush); err != nil {
		return err
	}
	if err := step("connector", &r.filterShadow, shadowF.Flush); err != nil {
		return err
	}
	if err := feed(); err != nil {
		return err
	}
	return cmp.finish()
}

// newFilter is Driver.NewStreamFilter without a Driver: a Strategy with no
// filter forwards every byte, as the cut-over Driver would.
func newFilter(s driverkit.Strategy, w io.Writer) driverkit.StreamFilter {
	if s.NewStreamFilter == nil {
		return forward{w}
	}
	return s.NewStreamFilter(w)
}

type forward struct{ io.Writer }

func (forward) Flush() error { return nil }

// regionCmp compares the Connector filter's output, as it arrives, with the
// native output ref, holding neither whole. On the first difference it keeps
// both outputs from the start of that line, and from then on only collects the
// Connector's side up to the cap.
type regionCmp struct {
	ref      *bufio.Reader
	line     int    // 1-based line of the next byte to compare
	prefix   []byte // matched head of the current line, last prefixMax bytes
	want     []byte
	diverged bool
	limit    int
	native   []byte
	shadow   []byte
}

func (c *regionCmp) feed(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	if c.diverged {
		c.shadow = appendCapped(c.shadow, p, c.limit)
		return nil
	}
	if cap(c.want) < len(p) {
		c.want = make([]byte, len(p))
	}
	want := c.want[:len(p)]
	n, err := io.ReadFull(c.ref, want)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return err
	}
	i := 0
	for i < n && want[i] == p[i] {
		i++
	}
	c.advance(p[:i])
	if i == len(p) {
		return nil
	}
	return c.diverge(want[i:n], p[i:])
}

// finish reports native output the Connector never produced.
func (c *regionCmp) finish() error {
	if c.diverged {
		return nil
	}
	if _, err := c.ref.Peek(1); err != nil {
		if err == io.EOF {
			return nil
		}
		return err
	}
	return c.diverge(nil, nil)
}

// diverge records the regions: both sides start with the matched head of the
// differing line, then the native bytes and the Connector bytes from the first
// difference, the native side topped up from its file.
func (c *regionCmp) diverge(native, shadow []byte) error {
	c.diverged = true
	c.limit = len(c.prefix) + regionMax
	c.native = appendCapped(append([]byte(nil), c.prefix...), native, c.limit)
	c.shadow = appendCapped(append([]byte(nil), c.prefix...), shadow, c.limit)
	rest, err := io.ReadAll(io.LimitReader(c.ref, int64(c.limit-len(c.native))))
	c.native = append(c.native, rest...)
	return err
}

// advance moves past matched bytes, tracking the line number and the matched
// head of the line it ends in.
func (c *regionCmp) advance(matched []byte) {
	c.line += bytes.Count(matched, []byte{'\n'})
	if i := bytes.LastIndexByte(matched, '\n'); i >= 0 {
		c.prefix = c.prefix[:0]
		matched = matched[i+1:]
	}
	c.prefix = append(c.prefix, matched...)
	if len(c.prefix) > prefixMax {
		c.prefix = append(c.prefix[:0], c.prefix[len(c.prefix)-prefixMax:]...)
	}
}

func appendCapped(dst, src []byte, limit int) []byte {
	if room := limit - len(dst); room < len(src) {
		src = src[:max(room, 0)]
	}
	return append(dst, src...)
}
