package connector

import (
	"bytes"
	"encoding/json"
)

// Sample is one method's call in a CheckDeclared run: the Params the request
// carries and the Answer the fake host gives each HTTP request the handler
// makes. Answer may be nil for a method that makes none.
type Sample struct {
	Params any
	Answer func(Request) (Response, error)
}

// CheckDeclared calls, for every capability the Connector registered, each
// method that capability serves, through the fake host, and fails t unless the
// Connector gives a real answer: a handler exists, the call returns a result
// rather than an error, and that result is a non-null JSON value. samples is
// keyed by method name and must cover every method a declared capability serves
// (a method the protocol marks optional, like forge.jobLog, is not required), so
// a Connector cannot declare a capability its checks never exercise. A
// capability the protocol does not define for a tracker or forge also fails.
//
// The SDK holds no result schema, so "real" stops at a decodable non-null
// result; shape checks belong to the Connector's own tests and core's
// conformance suite.
func (h *FakeHost) CheckDeclared(t TB, samples map[string]Sample) {
	t.Helper()
	for _, c := range capabilities {
		methods, ok := capabilityMethods[c]
		if !ok {
			t.Fatalf("connector declares capability %q, which the tracker and forge protocol does not define", c)
		}
		for _, m := range methods {
			h.checkMethod(t, c, m, samples)
		}
	}
}

func (h *FakeHost) checkMethod(t TB, capability, method string, samples map[string]Sample) {
	t.Helper()
	_, plain := handlers[method]
	if _, raw := rawHandlers[method]; !plain && !raw {
		t.Fatalf("connector declares capability %q but does not serve %q", capability, method)
	}
	s, ok := samples[method]
	if !ok {
		t.Fatalf("no sample for %q, which capability %q requires; add one to CheckDeclared", method, capability)
	}
	answer := s.Answer
	if answer == nil {
		answer = func(req Request) (Response, error) {
			t.Fatalf("%q made %s %s but its sample has no Answer", method, req.Method, req.URL)
			return Response{}, nil
		}
	}
	h.Serve(t, answer)

	req := struct {
		Method string `json:"method"`
		Params any    `json:"params,omitempty"`
	}{method, s.Params}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("sample params for %q do not encode: %v", method, err)
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *string         `json:"error"`
		Class  Class           `json:"class"`
	}
	if err := json.Unmarshal(call(raw), &resp); err != nil {
		t.Fatalf("%q answered something that is not a JSON object: %v", method, err)
	}
	if resp.Error != nil {
		t.Fatalf("capability %q: %q answered an error (%s): %s", capability, method, resp.Class, *resp.Error)
	}
	if len(resp.Result) == 0 || bytes.Equal(resp.Result, []byte("null")) {
		t.Fatalf("capability %q: %q answered no result", capability, method)
	}
}
