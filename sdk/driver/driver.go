// Package driver is the Driver author's side of the Driver capabilities
// (ADR 0073): a Connector declares a Strategy and Register exports it over
// the Connector protocol. The host builds a driverkit.Strategy from those
// exports.
//
// The host also passes its spindrift_op lines to a Decoder so its state can
// follow passes (see HostLine). It decodes their meaning itself and discards
// the Decoder's output for them.
package driver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"
	"unicode/utf8"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/event"
)

// StreamFilter sits between a Driver's stdout and its Pass-log tee. Flush
// writes out anything the filter still holds, and is safe to call repeatedly.
type StreamFilter interface {
	io.Writer
	Flush() error
}

// Decoder turns one Pass log line into the canonical events it carries.
type Decoder func(line string) []event.Event

// Reason classifies why a Pattern matched.
type Reason string

const (
	RateLimit       Reason = "rateLimit"
	Overloaded      Reason = "overloaded"
	Network         Reason = "network"
	TaskFailed      Reason = "taskFailed"
	UnsupportedFlag Reason = "unsupportedFlag"
)

// Pattern maps a log substring to the Reason it signals.
type Pattern struct {
	Substr string
	Reason Reason
}

// Strategy is everything that varies between Drivers.
type Strategy struct {
	// Name identifies the Driver; it must match a key in the nix lib/drivers/
	// registry.
	Name string

	// NewDecoder returns a fresh Decoder, called once per log the host reads;
	// the Decoder may keep state across lines. now is the host's wall clock
	// when the host sends one (the guest's own clock is a fixed fake epoch
	// under the host's wasm runtime); an absent or zero now falls back to the
	// guest's time.Now(), and a malformed one fails the call. A decoder that
	// resolves relative times resolves them against it.
	NewDecoder func(now time.Time) Decoder

	// TransientPatterns mark a failure worth retrying; TerminalPatterns one
	// that is not.
	TransientPatterns, TerminalPatterns []Pattern

	// WriteTool is the CLI's file-write tool name; "" tracks no writes.
	WriteTool string

	// ResultEvent encodes the final result text as one log line in the
	// CLI's own grammar.
	ResultEvent func(text string) ([]byte, error)

	// SessionFlags returns the CLI flags that start a session for the given
	// mode, repo, issue and home directory.
	SessionFlags func(mode, repoSlug, issue, home string) string

	// ResolveExit is optional and may rewrite the CLI's exit code. It takes
	// the Pass log's content, not a path: a Connector has no filesystem.
	ResolveExit func(log []byte, exitCode int) (int, error)

	// NewStreamFilter is optional. It wraps the writer the Pass log is teed
	// to with a filter over the CLI's stdout, called once per stream; nil is
	// the identity.
	NewStreamFilter func(w io.Writer) StreamFilter

	// Classify tunes how the host scans a failed Pass log for a Reason.
	Classify ClassifyOptions

	// Totals names where the run's token totals come from.
	Totals TotalsSource

	// Heartbeat opts the live heartbeat into phase and count lines.
	Heartbeat Heartbeat

	// DefaultTopLevelRole is the role reported for a model event with no role
	// of its own.
	DefaultTopLevelRole string

	// Result names how the final-message text is read from the log.
	Result ResultReader
}

// ClassifyOptions tunes classification for a CLI whose Pass log is not the
// default shape. The zero value skips oversized lines and lets the last
// unrecovered marker-bearing error win.
type ClassifyOptions struct {
	// ChunkOversized hands the Decoder each chunk of a line longer than the
	// scan buffer as its own line, for a CLI whose failure marker can ride a
	// huge line.
	ChunkOversized bool

	// FirstMatch latches the first unrecovered marker-bearing error instead of
	// the last.
	FirstMatch bool
}

// TotalsSource names where a Driver's run totals come from.
type TotalsSource int

const (
	// PerCallTotals sums every usage event. It is the zero value.
	PerCallTotals TotalsSource = iota
	// SessionTotals sums the result events: the CLI reports a summary per
	// session.
	SessionTotals
)

// ResultReader names how a Driver's final-message text is read from its log.
type ResultReader int

const (
	// ResultEvents reads every result event. It is the zero value.
	ResultEvents ResultReader = iota
	// LastMessage reads the last assistant message.
	LastMessage
)

// Heartbeat is the optional phase, kind and family tracking behind the live
// heartbeat. Each func is optional and is its own capability: no Kind yields
// no counts, no Phase an empty phase, and no Family labels a model by its
// exact id.
type Heartbeat struct {
	// Phase maps a tool call to the work phase it puts its role in. input is
	// the call's raw JSON arguments.
	Phase func(tool string, input json.RawMessage) string

	// Kind maps a tool call to the bucket its count line tallies it under.
	Kind func(tool string) string

	// KindOrder lists the kinds a count line names first, in this order; the
	// rest follow sorted.
	KindOrder []string

	// KindPlurals maps a kind to its irregular plural; others append "s".
	KindPlurals map[string]string

	// Family maps a model id to the label a heartbeat switch header shows and
	// the family's rank, which orders the final per-model table.
	Family func(model string) (label string, rank int)
}

// The enums cross as these names; each zero value is the default.
var (
	totalsWire = map[TotalsSource]string{PerCallTotals: "perCall", SessionTotals: "session"}
	resultWire = map[ResultReader]string{ResultEvents: "events", LastMessage: "lastMessage"}
)

// method serves one request. Its argument is the request's params bytes: JSON
// for every method but decodeRawMethod, whose bytes are the raw payload.
type method = func([]byte) (any, error)

// decodeRawMethod is registered with connector.HandleRaw, the rest with Handle.
const decodeRawMethod = "driver.decodeRaw"

// Register exports s as the Connector's Driver. Call it from init(). It
// panics if Name, NewDecoder, ResultEvent or SessionFlags is unset.
func Register(s Strategy) {
	caps, methods := handlers(s)
	connector.Register(caps...)
	for name, fn := range methods {
		if name == decodeRawMethod {
			connector.HandleRaw(name, fn)
		} else {
			connector.Handle(name, func(p json.RawMessage) (any, error) { return fn(p) })
		}
	}
}

type wirePattern struct {
	Substr string `json:"substr"`
	Reason string `json:"reason"`
}

type wireClassify struct {
	ChunkOversized bool `json:"chunkOversized"`
	FirstMatch     bool `json:"firstMatch"`
}

func wirePatterns(ps []Pattern) []wirePattern {
	out := make([]wirePattern, 0, len(ps)) // encode [] rather than null
	for _, p := range ps {
		out = append(out, wirePattern{p.Substr, string(p.Reason)})
	}
	return out
}

// errDecodePanic marks a decoder panic that decodeLine recovered.
var errDecodePanic = errors.New("decoder panicked")

// toValidUTF8 replaces each invalid byte of b with U+FFFD, as encoding/json
// does for a string.
func toValidUTF8(b []byte) []byte {
	if utf8.Valid(b) {
		return b
	}
	out := make([]byte, 0, len(b)+8)
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		out = utf8.AppendRune(out, r)
		b = b[n:]
	}
	return out
}

func unmarshal(params json.RawMessage, v any) error {
	if err := json.Unmarshal(params, v); err != nil {
		return fmt.Errorf("bad params: %w", err)
	}
	return nil
}

// handlers builds the capability list and method table for s. Decoder
// state lives in the returned closures, so each Register owns its handles.
func handlers(s Strategy) ([]string, map[string]method) {
	switch {
	case s.Name == "":
		panic("driver: Strategy.Name is required")
	case s.NewDecoder == nil:
		panic("driver: Strategy.NewDecoder is required")
	case s.ResultEvent == nil:
		panic("driver: Strategy.ResultEvent is required")
	case s.SessionFlags == nil:
		panic("driver: Strategy.SessionFlags is required")
	}

	decoders := map[uint32]Decoder{}
	var next uint32
	decodeLine := func(h uint32, line string) (evs []event.Event, err error) {
		dec, ok := decoders[h]
		if !ok {
			return nil, fmt.Errorf("unknown decoder %d", h)
		}
		// A panic would trap the guest, failing every later line too; as an
		// error it rejects this line's call alone.
		defer func() {
			if r := recover(); r != nil {
				evs, err = nil, fmt.Errorf("%w: %v", errDecodePanic, r)
			}
		}()
		evs = dec(line)
		if evs == nil {
			evs = []event.Event{} // encode [] rather than null
		}
		// driver.decodeRaw hands the decoder invalid UTF-8 a JSON transport
		// would have replaced, and Marshal copies a RawMessage's bytes as they
		// are, so the host would refuse the whole answer.
		for i := range evs {
			evs[i].ToolInput = toValidUTF8(evs[i].ToolInput)
		}
		return evs, nil
	}

	// batchElem is line's element in a many-line answer. A panicking line's
	// is not an events array, so the host rejects that element alone, as it
	// does a rejected driver.decode.
	batchElem := func(h uint32, line string) (any, error) {
		evs, err := decodeLine(h, line)
		if errors.Is(err, errDecodePanic) {
			return err.Error(), nil
		}
		return evs, err
	}

	m := map[string]method{
		"driver.describe": func([]byte) (any, error) {
			return struct {
				Name                string            `json:"name"`
				TransientPatterns   []wirePattern     `json:"transientPatterns"`
				TerminalPatterns    []wirePattern     `json:"terminalPatterns"`
				WriteTool           string            `json:"writeTool"`
				Classify            wireClassify      `json:"classify"`
				Totals              string            `json:"totals"`
				DefaultTopLevelRole string            `json:"defaultTopLevelRole,omitempty"`
				Result              string            `json:"result"`
				KindOrder           []string          `json:"kindOrder,omitempty"`
				KindPlurals         map[string]string `json:"kindPlurals,omitempty"`
			}{
				s.Name, wirePatterns(s.TransientPatterns), wirePatterns(s.TerminalPatterns), s.WriteTool,
				wireClassify(s.Classify), totalsWire[s.Totals], s.DefaultTopLevelRole, resultWire[s.Result],
				s.Heartbeat.KindOrder, s.Heartbeat.KindPlurals,
			}, nil
		},
		"driver.newDecoder": func(params []byte) (any, error) {
			var p struct {
				Now time.Time `json:"now"`
			}
			if len(params) > 0 {
				if err := unmarshal(params, &p); err != nil {
					return nil, err
				}
			}
			if p.Now.IsZero() {
				p.Now = time.Now()
			}
			next++
			decoders[next] = s.NewDecoder(p.Now)
			return struct {
				Decoder uint32 `json:"decoder"`
			}{next}, nil
		},
		"driver.decode": func(params []byte) (any, error) {
			var p struct {
				Decoder uint32 `json:"decoder"`
				Line    string `json:"line"`
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			return decodeLine(p.Decoder, p.Line)
		},
		"driver.decodeBatch": func(params []byte) (any, error) {
			var p struct {
				Decoder uint32   `json:"decoder"`
				Lines   []string `json:"lines"`
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			out := make([]any, len(p.Lines))
			for i, line := range p.Lines {
				elem, err := batchElem(p.Decoder, line)
				if err != nil {
					return nil, err
				}
				out[i] = elem
			}
			return out, nil
		},
		"driver.freeDecoder": func(params []byte) (any, error) {
			var p struct {
				Decoder uint32 `json:"decoder"`
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			delete(decoders, p.Decoder)
			return nil, nil
		},
		"driver.resultEvent": func(params []byte) (any, error) {
			var p struct {
				Text string `json:"text"`
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			b, err := s.ResultEvent(p.Text)
			if err != nil {
				return nil, err
			}
			return struct {
				Event string `json:"event"`
			}{string(b)}, nil
		},
		"driver.sessionFlags": func(params []byte) (any, error) {
			var p struct {
				Mode     string `json:"mode"`
				RepoSlug string `json:"repoSlug"`
				Issue    string `json:"issue"`
				Home     string `json:"home"`
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			return struct {
				Flags string `json:"flags"`
			}{s.SessionFlags(p.Mode, p.RepoSlug, p.Issue, p.Home)}, nil
		},
	}
	// The payload is "<decoder id>\n" then each line terminated by '\n', so a
	// line is any bytes but '\n' and nothing in it is JSON-escaped.
	m[decodeRawMethod] = func(payload []byte) (any, error) {
		head, rest, ok := bytes.Cut(payload, []byte{'\n'})
		if !ok {
			return nil, errors.New("bad payload: no decoder line")
		}
		h, err := strconv.ParseUint(string(head), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("bad payload: decoder id: %w", err)
		}
		out := make([]any, 0, bytes.Count(rest, []byte{'\n'}))
		for len(rest) > 0 {
			line, tail, ok := bytes.Cut(rest, []byte{'\n'})
			if !ok {
				return nil, errors.New("bad payload: final line has no newline")
			}
			elem, err := batchElem(uint32(h), string(line))
			if err != nil {
				return nil, err
			}
			out = append(out, elem)
			rest = tail
		}
		return out, nil
	}
	caps := []string{"driver", "driver.decodeBatch", decodeRawMethod}

	if s.ResolveExit != nil {
		caps = append(caps, "driver.resolveExit")
		m["driver.resolveExit"] = func(params []byte) (any, error) {
			var p struct {
				Log      string `json:"log"`
				ExitCode int    `json:"exitCode"`
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			code, err := s.ResolveExit([]byte(p.Log), p.ExitCode)
			if err != nil {
				return nil, err
			}
			return struct {
				ExitCode int `json:"exitCode"`
			}{code}, nil
		}
	}
	if s.NewStreamFilter != nil {
		caps = append(caps, "driver.streamFilter")
		type streamFilter struct {
			f   StreamFilter
			out *bytes.Buffer
		}
		filters := map[uint32]*streamFilter{}
		var nextFilter uint32
		// drain returns the bytes the filter wrote since the last call as a
		// non-nil slice, so an empty result encodes as "" rather than null.
		drain := func(sf *streamFilter) any {
			data := append([]byte{}, sf.out.Bytes()...)
			sf.out.Reset()
			return struct {
				Data []byte `json:"data"`
			}{data}
		}
		type filterParams struct {
			Filter uint32 `json:"filter"`
		}
		// run decodes params into p, whose id field names the filter, then
		// drains what step writes through it. The reset first keeps a failed
		// call's partial output from leading the next.
		run := func(params json.RawMessage, p any, id *uint32, step func(*streamFilter) error) (any, error) {
			if err := unmarshal(params, p); err != nil {
				return nil, err
			}
			sf, ok := filters[*id]
			if !ok {
				return nil, fmt.Errorf("unknown filter %d", *id)
			}
			sf.out.Reset()
			if err := step(sf); err != nil {
				return nil, err
			}
			return drain(sf), nil
		}
		m["driver.newStreamFilter"] = func([]byte) (any, error) {
			nextFilter++
			out := &bytes.Buffer{}
			filters[nextFilter] = &streamFilter{f: s.NewStreamFilter(out), out: out}
			return filterParams{nextFilter}, nil
		}
		m["driver.streamWrite"] = func(params []byte) (any, error) {
			var p struct {
				filterParams
				Data []byte `json:"data"`
			}
			return run(params, &p, &p.Filter, func(sf *streamFilter) error {
				_, err := sf.f.Write(p.Data)
				return err
			})
		}
		m["driver.streamFlush"] = func(params []byte) (any, error) {
			var p filterParams
			return run(params, &p, &p.Filter, func(sf *streamFilter) error { return sf.f.Flush() })
		}
		m["driver.freeStreamFilter"] = func(params []byte) (any, error) {
			var p filterParams
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			delete(filters, p.Filter)
			return nil, nil
		}
	}
	if h := s.Heartbeat; h.Phase != nil {
		caps = append(caps, "driver.toolPhase")
		m["driver.toolPhase"] = func(params []byte) (any, error) {
			var p struct {
				Tool  string          `json:"tool"`
				Input json.RawMessage `json:"input"`
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			return struct {
				Phase string `json:"phase"`
			}{h.Phase(p.Tool, p.Input)}, nil
		}
	}
	if h := s.Heartbeat; h.Kind != nil {
		caps = append(caps, "driver.toolKind")
		m["driver.toolKind"] = func(params []byte) (any, error) {
			var p struct {
				Tool string `json:"tool"`
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			return struct {
				Kind string `json:"kind"`
			}{h.Kind(p.Tool)}, nil
		}
	}
	if h := s.Heartbeat; h.Family != nil {
		caps = append(caps, "driver.modelFamily")
		m["driver.modelFamily"] = func(params []byte) (any, error) {
			var p struct {
				Model string `json:"model"`
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			label, rank := h.Family(p.Model)
			return struct {
				Label string `json:"label"`
				Rank  int    `json:"rank"`
			}{label, rank}, nil
		}
	}
	return caps, m
}
