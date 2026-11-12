//go:build !wasip1

package connector

import (
	"encoding/json"
	"errors"
	"testing"
)

// fakeHost swaps hostCall for one that records the request and answers with
// status and body.
func fakeHost(t *testing.T, status uint64, body string, got *Request) {
	t.Helper()
	saved := hostCall
	t.Cleanup(func() { hostCall = saved })
	hostCall = func(req, resp []byte) (uint64, error) {
		if got != nil {
			if err := json.Unmarshal(req, got); err != nil {
				t.Fatal(err)
			}
		}
		return status<<32 | uint64(copy(resp, body)), nil
	}
}

func TestHTTPRequestSendsTheWireFormAndReturnsTheAnswer(t *testing.T) {
	var got Request
	fakeHost(t, 201, "made", &got)
	want := Request{Method: "POST", URL: "https://h/p", Headers: map[string]string{"A": "b"}, Body: "x"}
	resp, err := HTTPRequest(want)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 201 || string(resp.Body) != "made" {
		t.Errorf("resp = %d %q, want 201 made", resp.Status, resp.Body)
	}
	if got.Method != want.Method || got.URL != want.URL || got.Headers["A"] != "b" || got.Body != "x" {
		t.Errorf("host saw %+v, want %+v", got, want)
	}
}

func TestHTTPRequestStatusZeroIsAnErrorCarryingTheText(t *testing.T) {
	fakeHost(t, 0, "dial refused", nil)
	_, err := HTTPRequest(Request{Method: "GET", URL: "https://h/"})
	if err == nil || err.Error() != "dial refused" {
		t.Fatalf("err = %v, want the host's text", err)
	}
}

func TestHTTPRequestRefusesALengthPastTheBuffer(t *testing.T) {
	saved := hostCall
	t.Cleanup(func() { hostCall = saved })
	hostCall = func(_, resp []byte) (uint64, error) { return 200<<32 | uint64(len(resp)+1), nil }
	if _, err := HTTPRequest(Request{Method: "GET", URL: "https://h/"}); err == nil {
		t.Fatal("err = nil, want a refusal")
	}
}

func TestHTTPRequestOutsideWasip1HasNoHost(t *testing.T) {
	if _, err := HTTPRequest(Request{Method: "GET", URL: "https://h/"}); !errors.Is(err, ErrNoHost) {
		t.Fatalf("err = %v, want ErrNoHost", err)
	}
}
