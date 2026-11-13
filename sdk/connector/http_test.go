//go:build !wasip1

package connector

import (
	"encoding/json"
	"errors"
	"strings"
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

func TestHTTPRequestBodyIsNotOverwrittenByALaterCall(t *testing.T) {
	fakeHost(t, 200, "first", nil)
	first, err := HTTPRequest(Request{Method: "GET", URL: "https://h/"})
	if err != nil {
		t.Fatal(err)
	}
	fakeHost(t, 200, "second", nil)
	if _, err := HTTPRequest(Request{Method: "GET", URL: "https://h/"}); err != nil {
		t.Fatal(err)
	}
	if string(first.Body) != "first" {
		t.Errorf("earlier Body = %q after a later call, want first", first.Body)
	}
}

// fakeHeaderHost swaps headerCall for one that answers with value, found or
// not, writing only as much as the buffer holds as the host does.
func fakeHeaderHost(t *testing.T, found bool, value string, gotName *string) {
	t.Helper()
	saved := headerCall
	t.Cleanup(func() { headerCall = saved })
	headerCall = func(name, out []byte) (uint64, error) {
		if gotName != nil {
			*gotName = string(name)
		}
		if !found {
			return 0, nil
		}
		copy(out, value)
		return 1<<32 | uint64(len(value)), nil
	}
}

func TestResponseHeaderReturnsTheValue(t *testing.T) {
	var name string
	fakeHeaderHost(t, true, "repo, read:org", &name)
	got, ok := ResponseHeader("X-OAuth-Scopes")
	if !ok || got != "repo, read:org" || name != "X-OAuth-Scopes" {
		t.Errorf("ResponseHeader = %q, %v (host saw %q), want the value", got, ok, name)
	}
}

func TestResponseHeaderEmptyValueIsPresent(t *testing.T) {
	fakeHeaderHost(t, true, "", nil)
	if got, ok := ResponseHeader("X-OAuth-Scopes"); !ok || got != "" {
		t.Errorf("ResponseHeader = %q, %v, want an empty present value", got, ok)
	}
}

func TestResponseHeaderAbsent(t *testing.T) {
	fakeHeaderHost(t, false, "", nil)
	if got, ok := ResponseHeader("X-Nope"); ok || got != "" {
		t.Errorf("ResponseHeader = %q, %v, want absent", got, ok)
	}
}

// A value longer than the first buffer is fetched again with room for it.
func TestResponseHeaderGrowsForALongValue(t *testing.T) {
	long := strings.Repeat("a", 3*headerBufStart)
	fakeHeaderHost(t, true, long, nil)
	if got, ok := ResponseHeader("Link"); !ok || got != long {
		t.Errorf("ResponseHeader = %d bytes, %v, want the %d byte value", len(got), ok, len(long))
	}
}

func TestResponseHeaderOutsideWasip1HasNoHost(t *testing.T) {
	if got, ok := ResponseHeader("X"); ok || got != "" {
		t.Errorf("ResponseHeader = %q, %v, want absent without a host", got, ok)
	}
}
