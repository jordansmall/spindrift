//go:build integration

package piseam

import (
	"strings"
	"testing"
)

// Failed turns in JSON mode exit zero: the failure is an assistant message
// with stopReason "error", and the stream still ends in agent_settled.
func TestPiErrorExit(t *testing.T) {
	const rateLimit = `{"error":{"message":"Rate limit reached","type":"rate_limit_error","code":"rate_limit_exceeded"}}`
	for _, tc := range []struct {
		name         string
		script       []reply
		wantRequests int
		wantRetryEnd bool
		wantMessage  string
	}{
		{
			name:         "400 is not retried",
			script:       []reply{statusReply(400, `{"error":{"message":"bad things","type":"invalid_request_error"}}`)},
			wantRequests: 1,
			wantMessage:  "400: ",
		},
		{
			name:         "401 is not retried",
			script:       []reply{statusReply(401, `{"error":{"message":"Incorrect API key","type":"invalid_request_error","code":"invalid_api_key"}}`)},
			wantRequests: 1,
			wantMessage:  "401: ",
		},
		{
			name:         "429 exhausts the retries",
			script:       []reply{statusReply(429, rateLimit), statusReply(429, rateLimit), statusReply(429, rateLimit), statusReply(429, rateLimit)},
			wantRequests: 4,
			wantRetryEnd: true,
			wantMessage:  "429: ",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, tc.script...)
			r := runPi(t, s, "Say hello.")
			if r.err != nil {
				t.Fatalf("pi exit = %s, want 0 (JSON-mode failed turns exit zero)\nstderr:\n%s", exitDetail(r.err), r.stderr)
			}
			if n := len(s.Requests()); n != tc.wantRequests {
				t.Errorf("stub got %d requests, want %d", n, tc.wantRequests)
			}
			msg := lastAssistantMessage(r.events)
			if msg["stopReason"] != "error" {
				t.Errorf("last assistant stopReason = %v, want error", msg["stopReason"])
			}
			if got, _ := msg["errorMessage"].(string); !strings.HasPrefix(got, tc.wantMessage) {
				t.Errorf("last assistant errorMessage = %q, want prefix %q", got, tc.wantMessage)
			}
			ends := eventsOfType(r.events, "auto_retry_end")
			if tc.wantRetryEnd {
				if len(ends) != 1 || ends[0]["success"] != false || ends[0]["attempt"] != float64(3) {
					t.Errorf("auto_retry_end events = %v, want one with success=false attempt=3", ends)
				}
			} else if len(ends) != 0 {
				t.Errorf("got %d auto_retry_end events, want none", len(ends))
			}
			last := r.events[len(r.events)-1]
			if last["type"] != "agent_settled" || last["aborted"] != false {
				t.Errorf("last event = %v, want agent_settled with aborted=false", last)
			}
		})
	}
}

func TestPiUnknownModel(t *testing.T) {
	t.Run("unknown provider fails before any event", func(t *testing.T) {
		s := newStub(t)
		r := runPiWith(t, s, piOpts{args: []string{"--model", "nope/nope"}}, "hi")
		if exitDetail(r.err) != "exit 1" {
			t.Errorf("pi exit = %s, want exit 1", exitDetail(r.err))
		}
		if !strings.Contains(r.stderr, `Model "nope/nope" not found`) {
			t.Errorf("stderr = %q, want a model-not-found error", r.stderr)
		}
		if len(r.events) != 0 || len(s.Requests()) != 0 {
			t.Errorf("got %d events and %d requests, want none", len(r.events), len(s.Requests()))
		}
	})
	// Under a configured provider an unlisted id is not an error: pi warns and
	// sends the id to the endpoint as-is.
	t.Run("unknown id under a configured provider is sent anyway", func(t *testing.T) {
		s := newStub(t, textReply("ok"))
		r := runPiWith(t, s, piOpts{args: []string{"--model", stubProvider + "/nope"}}, "hi")
		if r.err != nil {
			t.Fatalf("pi exit = %s, want 0\nstderr:\n%s", exitDetail(r.err), r.stderr)
		}
		if !strings.Contains(r.stderr, `Model "nope" not found for provider "stub". Using custom model id.`) {
			t.Errorf("stderr = %q, want the custom-model-id warning", r.stderr)
		}
		reqs := s.Requests()
		if len(reqs) != 1 || reqs[0]["model"] != "nope" {
			t.Errorf("stub requests = %v, want one with model nope", reqs)
		}
	})
}
