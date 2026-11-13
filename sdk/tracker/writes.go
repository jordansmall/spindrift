package tracker

import (
	"encoding/json"

	"spindrift.dev/sdk/connector"
)

// The capability names of the write primitives. Each is declared only when
// the Connector sets the funcs behind it. Core keeps its own copies; a parity
// test pins them.
const (
	CapTransition = "tracker.transition"
	CapEditLabels = "tracker.editLabels"
	CapLabels     = "tracker.labels"

	// The methods behind CapLabels; they ride on it rather than being
	// advertised themselves.
	CapListLabels  = "tracker.listLabels"
	CapCreateLabel = "tracker.createLabel"
)

// StatusTarget is the tracker status a Dispatch state maps to and the
// resolution to set on entering it ("" for none).
type StatusTarget struct {
	Status     string `json:"status"`
	Resolution string `json:"resolution"`
}

// TransitionRequest asks a Connector to move an issue to a Dispatch state.
// State and the keys of Mapping are core's canonical state names (dispatchable,
// in-progress, complete, failed, ambiguous, ...). A Connector has no config
// channel, so core ships the whole table on every call and the Connector
// applies Mapping[State], including the rule that only failed and ambiguous
// may share a status. Mapping holds only the states that name a status.
type TransitionRequest struct {
	State   string                  `json:"state"`
	Mapping map[string]StatusTarget `json:"mapping"`
}

// The outcomes of a Transition that is not an error.
const (
	// TransitionMoved means the issue now sits in the requested status.
	TransitionMoved = "moved"
	// TransitionUnavailable means the issue's workflow offers no transition to
	// the resolved Status; Offered names the statuses it does offer.
	TransitionUnavailable = "unavailable"
	// TransitionUnmapped means Mapping holds no non-empty status for State.
	TransitionUnmapped = "unmapped"
)

// TransitionResult is how a Transition ended. Status is the tracker status the
// Connector resolved State to ("" when unmapped), so core can name it in a
// refusal. Any other failure, such as a transport error or a transition that
// needs a field the request cannot supply, is an error, never an outcome.
type TransitionResult struct {
	Outcome string   `json:"outcome"`
	Status  string   `json:"status"`
	Offered []string `json:"offered"`
}

// Writer is the write primitives of a Connector-backed tracker. Each func is
// optional and declares its capability only when set. Core decides lifecycle
// policy (fallback, refusal, claim checks, verdicts); a Writer applies the
// mapping it is sent and performs the one write.
type Writer struct {
	// Transition declares CapTransition. It moves the issue to the status
	// r.Mapping names for r.State, applying that entry's resolution where the
	// tracker takes one.
	Transition func(t Target, number string, r TransitionRequest) (TransitionResult, error)

	// EditLabels declares CapEditLabels. It adds and removes labels on the
	// issue in one write.
	EditLabels func(t Target, number string, add, remove []string) error

	// ListLabels declares CapLabels. It returns the names of the labels the
	// tracker's registry holds.
	ListLabels func(t Target) ([]string, error)

	// CreateLabel adds a label to the registry. Leave it nil for a tracker
	// with no label registry: core's create call is then answered as a no-op.
	// It requires ListLabels.
	CreateLabel func(t Target, name, description, color string) error
}

// RegisterWrites exports w as the Connector's tracker write primitives. Call
// it from init(). It panics if CreateLabel is set without ListLabels.
func RegisterWrites(w Writer) {
	caps, methods := writeHandlers(w)
	connector.Register(caps...)
	for name, fn := range methods {
		connector.Handle(name, fn)
	}
}

// writeHandlers builds the capability list and method table for w.
func writeHandlers(w Writer) ([]string, map[string]method) {
	if w.CreateLabel != nil && w.ListLabels == nil {
		panic("tracker: Writer.CreateLabel requires Writer.ListLabels")
	}
	// done runs a call with no result, answering an empty object, not null.
	done := func(err error) (any, error) {
		if err != nil {
			return nil, err
		}
		return struct{}{}, nil
	}
	m := map[string]method{}
	var caps []string

	if w.Transition != nil {
		caps = append(caps, CapTransition)
		m[CapTransition] = func(params json.RawMessage) (any, error) {
			var p struct {
				Target Target `json:"target"`
				Number string `json:"number"`
				TransitionRequest
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			res, err := w.Transition(p.Target, p.Number, p.TransitionRequest)
			if err != nil {
				return nil, err
			}
			if res.Offered == nil {
				res.Offered = []string{} // encode [] rather than null
			}
			return res, nil
		}
	}
	if w.EditLabels != nil {
		caps = append(caps, CapEditLabels)
		m[CapEditLabels] = func(params json.RawMessage) (any, error) {
			var p struct {
				Target Target   `json:"target"`
				Number string   `json:"number"`
				Add    []string `json:"add"`
				Remove []string `json:"remove"`
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			return done(w.EditLabels(p.Target, p.Number, p.Add, p.Remove))
		}
	}
	if w.ListLabels != nil {
		caps = append(caps, CapLabels)
		m[CapListLabels] = func(params json.RawMessage) (any, error) {
			t, err := targetOnly(params)
			if err != nil {
				return nil, err
			}
			names, err := w.ListLabels(t)
			if err != nil {
				return nil, err
			}
			if names == nil {
				names = []string{} // encode [] rather than null
			}
			return struct {
				Labels []string `json:"labels"`
			}{names}, nil
		}
		m[CapCreateLabel] = func(params json.RawMessage) (any, error) {
			var p struct {
				Target      Target `json:"target"`
				Name        string `json:"name"`
				Description string `json:"description"`
				Color       string `json:"color"`
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			if w.CreateLabel == nil {
				return done(nil)
			}
			return done(w.CreateLabel(p.Target, p.Name, p.Description, p.Color))
		}
	}
	return caps, m
}
