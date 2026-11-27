//go:build !wasip1

package connector

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// declare swaps the package's registry for the test: the capabilities and the
// handlers a Connector's init() would have registered.
func declare(t *testing.T, caps []string, hs map[string]func(json.RawMessage) (any, error)) {
	t.Helper()
	savedCaps, savedHandlers, savedRaw := capabilities, handlers, rawHandlers
	t.Cleanup(func() { capabilities, handlers, rawHandlers = savedCaps, savedHandlers, savedRaw })
	capabilities, handlers, rawHandlers = caps, hs, nil
}

var answerOK = func(json.RawMessage) (any, error) {
	return map[string]any{"ok": true}, nil
}

// checkDeclared runs CheckDeclared against a stub TB and returns what it failed with.
func checkDeclared(t *testing.T, samples map[string]Sample) string {
	t.Helper()
	tb := &stubTB{}
	inGoroutine(func() { NewFakeHost(testRoutes).CheckDeclared(tb, samples) })
	tb.runCleanups()
	return tb.fatal
}

func TestCheckDeclaredPassesAServedCapability(t *testing.T) {
	declare(t, []string{"tracker.blocks"}, map[string]func(json.RawMessage) (any, error){"tracker.blocks": answerOK})
	if got := checkDeclared(t, map[string]Sample{"tracker.blocks": {Params: map[string]any{"number": "1"}}}); got != "" {
		t.Errorf("failed: %s", got)
	}
}

func TestCheckDeclaredServesTheHandlersHTTPRequestsFromTheSample(t *testing.T) {
	declare(t, []string{"tracker.blocks"}, map[string]func(json.RawMessage) (any, error){
		"tracker.blocks": func(json.RawMessage) (any, error) {
			resp, err := HTTPRequest(Request{Method: "GET", URL: "https://jira.example.com/rest/api/2"})
			if err != nil {
				return nil, err
			}
			return string(resp.Body), nil
		},
	})
	got := checkDeclared(t, map[string]Sample{"tracker.blocks": {Answer: func(Request) (Response, error) {
		return Response{Status: 200, Body: []byte("x")}, nil
	}}})
	if got != "" {
		t.Errorf("failed: %s", got)
	}
}

func TestCheckDeclaredFailsACapabilityWithNoHandler(t *testing.T) {
	declare(t, []string{"tracker.blocks"}, nil)
	got := checkDeclared(t, map[string]Sample{"tracker.blocks": {}})
	if want := `connector declares capability "tracker.blocks" but does not serve "tracker.blocks"`; got != want {
		t.Errorf("fatal = %q, want %q", got, want)
	}
}

func TestCheckDeclaredFailsAMethodWithNoSample(t *testing.T) {
	declare(t, []string{"tracker.blocks"}, map[string]func(json.RawMessage) (any, error){"tracker.blocks": answerOK})
	got := checkDeclared(t, nil)
	if !strings.Contains(got, `no sample for "tracker.blocks"`) {
		t.Errorf("fatal = %q, want the missing sample named", got)
	}
}

func TestCheckDeclaredFailsAnErrorAnswerWithItsClass(t *testing.T) {
	declare(t, []string{"tracker.blocks"}, map[string]func(json.RawMessage) (any, error){
		"tracker.blocks": func(json.RawMessage) (any, error) { return nil, &Error{Class: NotFound, Err: errors.New("gone")} },
	})
	got := checkDeclared(t, map[string]Sample{"tracker.blocks": {}})
	if !strings.Contains(got, "(not-found): gone") {
		t.Errorf("fatal = %q, want the class and message", got)
	}
}

func TestCheckDeclaredFailsANullResult(t *testing.T) {
	declare(t, []string{"tracker.blocks"}, map[string]func(json.RawMessage) (any, error){
		"tracker.blocks": func(json.RawMessage) (any, error) { return nil, nil },
	})
	got := checkDeclared(t, map[string]Sample{"tracker.blocks": {}})
	if !strings.Contains(got, `"tracker.blocks" answered no result`) {
		t.Errorf("fatal = %q, want a no-result failure", got)
	}
}

func TestCheckDeclaredFailsAnUnknownCapability(t *testing.T) {
	declare(t, []string{"tracker.nope"}, nil)
	got := checkDeclared(t, nil)
	if !strings.Contains(got, `capability "tracker.nope"`) {
		t.Errorf("fatal = %q, want the capability named", got)
	}
}

func TestCheckDeclaredRequiresEveryMethodOfACapability(t *testing.T) {
	declare(t, []string{"tracker.count"}, map[string]func(json.RawMessage) (any, error){"tracker.count": answerOK})
	got := checkDeclared(t, map[string]Sample{"tracker.count": {}, "tracker.countInterval": {}})
	if !strings.Contains(got, `does not serve "tracker.countInterval"`) {
		t.Errorf("fatal = %q, want the unserved sibling method", got)
	}
}

func TestCheckDeclaredPassesACapabilityThatServesNoMethod(t *testing.T) {
	declare(t, []string{"tracker.ids.projectKey"}, nil)
	if got := checkDeclared(t, nil); got != "" {
		t.Errorf("a capability that serves no method failed: %s", got)
	}
}

func TestCheckDeclaredDoesNotRequireAnOptionalMethod(t *testing.T) {
	served := map[string]func(json.RawMessage) (any, error){}
	samples := map[string]Sample{}
	for _, m := range capabilityMethods["forge"] {
		served[m] = answerOK
		samples[m] = Sample{}
	}
	declare(t, []string{"forge"}, served)
	if got := checkDeclared(t, samples); got != "" {
		t.Errorf("a forge without forge.jobLog failed: %s", got)
	}
}
