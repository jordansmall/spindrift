//go:build !wasip1

package connector

import (
	"errors"
	"strconv"
	"testing"
)

var jsonRoutes = []Route{
	{Method: "GET", Host: "api.example.com", Path: "/"},
	{Method: "POST", Host: "api.example.com", Path: "/"},
}

func classOf(t *testing.T, err error) Class {
	t.Helper()
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want an *Error", err)
	}
	return ce.Class
}

type thing struct {
	Name string `json:"name"`
}

func TestSendJSONStatus(t *testing.T) {
	tests := []struct {
		status int
		body   string
		want   Class // empty: success
	}{
		{200, `{"name":"x"}`, ""},
		{201, `{"name":"x"}`, ""},
		{404, ``, NotFound},
		{401, ``, Auth},
		{403, ``, Auth},
		{429, ``, RateLimited},
		{408, ``, Transient},
		{500, ``, Transient},
		{503, ``, Transient},
		{400, ``, Permanent},
		{409, ``, Permanent},
		{422, ``, Permanent},
	}
	for _, tc := range tests {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			NewFakeHost(jsonRoutes).Serve(t, func(Request) (Response, error) {
				return Response{Status: tc.status, Body: []byte(tc.body)}, nil
			})
			var out thing
			resp, err := SendJSON(Request{Method: "GET", URL: "https://api.example.com/x"}, nil, &out)
			if resp.Status != tc.status {
				t.Errorf("resp.Status = %d, want %d", resp.Status, tc.status)
			}
			if tc.want == "" {
				if err != nil || out.Name != "x" {
					t.Fatalf("out = %+v, err = %v, want name x", out, err)
				}
				return
			}
			if got := classOf(t, err); got != tc.want {
				t.Errorf("class = %q, want %q", got, tc.want)
			}
			if want := "GET https://api.example.com/x: status " + strconv.Itoa(tc.status); err.Error() != want {
				t.Errorf("err = %q, want %q", err, want)
			}
		})
	}
}

func TestSendJSON2xxWithoutOut(t *testing.T) {
	for _, status := range []int{200, 204} {
		NewFakeHost(jsonRoutes).Serve(t, func(Request) (Response, error) {
			return Response{Status: status, Body: []byte("not json")}, nil
		})
		resp, err := SendJSON(Request{Method: "GET", URL: "https://api.example.com/x"}, nil, nil)
		if err != nil || resp.Status != status {
			t.Errorf("status %d: resp = %+v, err = %v", status, resp, err)
		}
	}
}

func TestSendJSONUndecodable(t *testing.T) {
	for _, body := range []string{"", "not json", `{"name":1}`} {
		NewFakeHost(jsonRoutes).Serve(t, func(Request) (Response, error) {
			return Response{Status: 200, Body: []byte(body)}, nil
		})
		var out thing
		resp, err := SendJSON(Request{Method: "GET", URL: "https://api.example.com/x"}, nil, &out)
		if got := classOf(t, err); got != Permanent {
			t.Errorf("body %q: class = %q, want permanent", body, got)
		}
		if resp.Status != 200 {
			t.Errorf("body %q: resp.Status = %d, want 200", body, resp.Status)
		}
	}
}

func TestSendJSONTransportFailure(t *testing.T) {
	NewFakeHost(jsonRoutes).Serve(t, func(Request) (Response, error) {
		return Response{}, errors.New("connection reset")
	})
	_, err := SendJSON(Request{Method: "GET", URL: "https://api.example.com/x"}, nil, nil)
	if got := classOf(t, err); got != Transient {
		t.Errorf("class = %q, want transient", got)
	}
	if want := "GET https://api.example.com/x: connection reset"; err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
}

func TestSendJSONUnencodable(t *testing.T) {
	sent := NewFakeHost(jsonRoutes).Serve(t, func(Request) (Response, error) {
		return Response{Status: 200}, nil
	})
	_, err := SendJSON(Request{Method: "POST", URL: "https://api.example.com/x"}, make(chan int), nil)
	if got := classOf(t, err); got != Permanent {
		t.Errorf("class = %q, want permanent", got)
	}
	if len(*sent) != 0 {
		t.Errorf("sent %d requests, want none", len(*sent))
	}
}

func TestSendJSONHeadersAndBody(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		headers map[string]string
		in      any
		want    map[string]string
		body    string
	}{
		{"no body", "GET", nil, nil,
			map[string]string{"Accept": "application/json"}, ""},
		{"body", "POST", nil, thing{Name: "x"},
			map[string]string{"Accept": "application/json", "Content-Type": "application/json"}, `{"name":"x"}`},
		{"caller headers kept", "POST", map[string]string{"X-Token": "t"}, thing{Name: "x"},
			map[string]string{"X-Token": "t", "Accept": "application/json", "Content-Type": "application/json"}, `{"name":"x"}`},
		{"caller Accept kept, any case", "GET", map[string]string{"accept": "application/vnd.api+json"}, nil,
			map[string]string{"accept": "application/vnd.api+json"}, ""},
		{"caller Content-Type kept", "POST", map[string]string{"Content-Type": "application/merge-patch+json"}, thing{Name: "x"},
			map[string]string{"Content-Type": "application/merge-patch+json", "Accept": "application/json"}, `{"name":"x"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sent := NewFakeHost(jsonRoutes).Serve(t, func(Request) (Response, error) {
				return Response{Status: 200}, nil
			})
			orig := map[string]string{}
			for k, v := range tc.headers {
				orig[k] = v
			}
			if _, err := SendJSON(Request{Method: tc.method, URL: "https://api.example.com/x", Headers: tc.headers}, tc.in, nil); err != nil {
				t.Fatal(err)
			}
			got := (*sent)[0]
			if got.Body != tc.body {
				t.Errorf("body = %q, want %q", got.Body, tc.body)
			}
			if len(got.Headers) != len(tc.want) {
				t.Errorf("headers = %v, want %v", got.Headers, tc.want)
			}
			for k, v := range tc.want {
				if got.Headers[k] != v {
					t.Errorf("header %s = %q, want %q in %v", k, got.Headers[k], v, got.Headers)
				}
			}
			if len(tc.headers) != len(orig) {
				t.Errorf("caller's headers mutated: %v, was %v", tc.headers, orig)
			}
		})
	}
}
