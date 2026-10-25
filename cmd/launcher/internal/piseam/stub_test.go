//go:build integration

package piseam

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// reply is one scripted answer to one POST /v1/chat/completions.
type reply struct {
	status int // 0 means 200 with an SSE stream of chunks
	body   string
	chunks []map[string]any
	// delay is how long the stub waits before sending its first byte; a client
	// that gives up sooner cuts the wait short.
	delay time.Duration
}

func textReply(text string) reply {
	return reply{chunks: []map[string]any{
		delta(map[string]any{"role": "assistant", "content": text}, nil),
		finalDelta("stop"),
	}}
}

// reasoningTextReply streams reasoning_content, which pi maps to a thinking
// content block, ahead of the answer text.
func reasoningTextReply(reasoning, text string) reply {
	return reply{chunks: []map[string]any{
		delta(map[string]any{"role": "assistant", "reasoning_content": reasoning}, nil),
		delta(map[string]any{"content": text}, nil),
		finalDelta("stop"),
	}}
}

// finalDelta closes a successful reply with finish_reason and a usage block:
// 120 prompt tokens of which 40 were cached, 30 completion tokens of which 10
// were reasoning, so pi reports input 80, cacheRead 40, output 30.
func finalDelta(finish string) map[string]any {
	c := delta(map[string]any{}, finish)
	c["usage"] = map[string]any{
		"prompt_tokens": 120, "completion_tokens": 30, "total_tokens": 150,
		"prompt_tokens_details":     map[string]any{"cached_tokens": 40},
		"completion_tokens_details": map[string]any{"reasoning_tokens": 10},
	}
	return c
}

func toolCallReply(id, name, argsJSON string) reply {
	return reply{chunks: []map[string]any{
		delta(map[string]any{"role": "assistant", "tool_calls": []map[string]any{{
			"index": 0, "id": id, "type": "function",
			"function": map[string]any{"name": name, "arguments": argsJSON},
		}}}, nil),
		finalDelta("tool_calls"),
	}}
}

func statusReply(status int, body string) reply { return reply{status: status, body: body} }

func delta(d map[string]any, finish any) map[string]any {
	return map[string]any{
		"id": "chatcmpl-stub", "object": "chat.completion.chunk", "created": 0, "model": stubModel,
		"choices": []map[string]any{{"index": 0, "delta": d, "finish_reason": finish}},
	}
}

// stub serves the scripted replies in order, one per request, and records each
// request body and headers.
type stub struct {
	*httptest.Server
	mu       sync.Mutex
	script   []reply
	requests []map[string]any
	headers  []http.Header
	// pick, when set, chooses the reply from the request, and the script is
	// not used. Concurrent children reach the Provider in no fixed order, so a
	// parallel run needs routing by content, not by arrival.
	pick func(req map[string]any) reply
}

// newRoutedStub serves each request the reply pick returns for it.
func newRoutedStub(t *testing.T, pick func(req map[string]any) reply) *stub {
	t.Helper()
	s := newStub(t)
	s.pick = pick
	return s
}

func newStub(t *testing.T, script ...reply) *stub {
	t.Helper()
	s := &stub{script: script}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.serve(t, w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *stub) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/chat/completions" {
		t.Errorf("stub: unexpected path %s", r.URL.Path)
		http.NotFound(w, r)
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("stub: reading request body: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Errorf("stub: undecodable request body: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.headers = append(s.headers, r.Header.Clone())
	n := len(s.requests)
	var rep reply
	ok := len(s.script) > 0
	if s.pick != nil {
		rep, ok = s.pick(req), true
	} else if ok {
		rep, s.script = s.script[0], s.script[1:]
	}
	s.mu.Unlock()
	if !ok {
		t.Errorf("stub: request %d arrived with the script exhausted", n)
		http.Error(w, "script exhausted", http.StatusInternalServerError)
		return
	}
	if rep.delay > 0 {
		select {
		case <-time.After(rep.delay):
		case <-r.Context().Done():
			return
		}
	}
	if rep.status != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range rep.chunks {
		b, err := json.Marshal(c)
		if err != nil {
			t.Errorf("stub: marshal chunk: %v", err)
			return
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

// Requests returns a copy of the request bodies received so far.
func (s *stub) Requests() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.requests...)
}

// Headers returns a copy of the request headers received so far, in request order.
func (s *stub) Headers() []http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]http.Header(nil), s.headers...)
}
