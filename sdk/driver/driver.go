// Package driver is the Driver author's side of the Driver capabilities
// (ADR 0073): a Connector declares a Strategy and Register exports it over
// the Connector protocol. The host builds a driverkit.Strategy from those
// exports.
//
// spindrift_op host lines never reach a Decoder: the host decodes its own
// lines, so a Driver's decoder sees only its CLI's output.
package driver

import (
	"encoding/json"
	"fmt"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/event"
)

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
	// the Decoder may keep state across lines.
	NewDecoder func() Decoder

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
}

type method = func(json.RawMessage) (any, error)

// Register exports s as the Connector's Driver. Call it from init(). It
// panics if Name, NewDecoder, ResultEvent or SessionFlags is unset.
func Register(s Strategy) {
	caps, methods := handlers(s)
	connector.Register(caps...)
	for name, fn := range methods {
		connector.Handle(name, fn)
	}
}

type wirePattern struct {
	Substr string `json:"substr"`
	Reason string `json:"reason"`
}

func wirePatterns(ps []Pattern) []wirePattern {
	out := make([]wirePattern, 0, len(ps)) // encode [] rather than null
	for _, p := range ps {
		out = append(out, wirePattern{p.Substr, string(p.Reason)})
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

	m := map[string]method{
		"driver.describe": func(json.RawMessage) (any, error) {
			return struct {
				Name              string        `json:"name"`
				TransientPatterns []wirePattern `json:"transientPatterns"`
				TerminalPatterns  []wirePattern `json:"terminalPatterns"`
				WriteTool         string        `json:"writeTool"`
			}{s.Name, wirePatterns(s.TransientPatterns), wirePatterns(s.TerminalPatterns), s.WriteTool}, nil
		},
		"driver.newDecoder": func(json.RawMessage) (any, error) {
			next++
			decoders[next] = s.NewDecoder()
			return struct {
				Decoder uint32 `json:"decoder"`
			}{next}, nil
		},
		"driver.decode": func(params json.RawMessage) (any, error) {
			var p struct {
				Decoder uint32 `json:"decoder"`
				Line    string `json:"line"`
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			dec, ok := decoders[p.Decoder]
			if !ok {
				return nil, fmt.Errorf("unknown decoder %d", p.Decoder)
			}
			evs := dec(p.Line)
			if evs == nil {
				evs = []event.Event{} // encode [] rather than null
			}
			return evs, nil
		},
		"driver.freeDecoder": func(params json.RawMessage) (any, error) {
			var p struct {
				Decoder uint32 `json:"decoder"`
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			delete(decoders, p.Decoder)
			return nil, nil
		},
		"driver.resultEvent": func(params json.RawMessage) (any, error) {
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
		"driver.sessionFlags": func(params json.RawMessage) (any, error) {
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
	caps := []string{"driver"}

	if s.ResolveExit != nil {
		caps = append(caps, "driver.resolveExit")
		m["driver.resolveExit"] = func(params json.RawMessage) (any, error) {
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
	return caps, m
}
