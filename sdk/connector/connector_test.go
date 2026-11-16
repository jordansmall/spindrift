package connector

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
)

func resetForTest(t *testing.T) {
	t.Helper()
	saved := capabilities
	t.Cleanup(func() { capabilities = saved })
	capabilities = nil
}

func TestHandshake(t *testing.T) {
	cases := []struct {
		name     string
		register [][]string
		want     []string
	}{
		{"none", nil, nil},
		{"one call", [][]string{{"a", "b"}}, []string{"a", "b"}},
		{"accumulates", [][]string{{"a"}, {"b", "c"}}, []string{"a", "b", "c"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetForTest(t)
			for _, caps := range c.register {
				Register(caps...)
			}
			raw, err := handshakeJSON()
			if err != nil {
				t.Fatal(err)
			}
			var got handshakeResult
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("not valid JSON: %v", err)
			}
			if got.ProtocolVersion != protocolVersion {
				t.Errorf("protocolVersion = %q, want %q", got.ProtocolVersion, protocolVersion)
			}
			if !slices.Equal(got.Capabilities, c.want) {
				t.Errorf("capabilities = %v, want %v", got.Capabilities, c.want)
			}
		})
	}
}

func resetHandlers(t *testing.T) {
	t.Helper()
	saved, savedRaw := handlers, rawHandlers
	t.Cleanup(func() { handlers, rawHandlers = saved, savedRaw })
	handlers, rawHandlers = nil, nil
}

func TestCall(t *testing.T) {
	resetHandlers(t)
	Handle("echo", func(p json.RawMessage) (any, error) { return p, nil })
	Handle("fail", func(json.RawMessage) (any, error) { return nil, errors.New("boom") })
	Handle("slow", func(json.RawMessage) (any, error) { return nil, Errorf(Transient, "try later") })
	Handle("wrapped", func(json.RawMessage) (any, error) {
		return nil, fmt.Errorf("ctx: %w", &Error{RateLimited, errors.New("slow down")})
	})
	Handle("none", func(json.RawMessage) (any, error) { return nil, nil })
	Handle("sum", func(p json.RawMessage) (any, error) {
		var in []int
		if err := json.Unmarshal(p, &in); err != nil {
			return nil, err
		}
		t := 0
		for _, n := range in {
			t += n
		}
		return map[string]int{"total": t}, nil
	})

	cases := []struct{ name, req, want string }{
		{"echo", `{"method":"echo","params":{"a":[1,2]}}`, `{"result":{"a":[1,2]}}`},
		{"fail", `{"method":"fail","params":null}`, `{"error":"boom","class":"permanent"}`},
		{"classed", `{"method":"slow"}`, `{"error":"try later","class":"transient"}`},
		{"classed wrapped", `{"method":"wrapped"}`, `{"error":"ctx: slow down","class":"rate-limited"}`},
		{"nil result", `{"method":"none"}`, `{"result":null}`},
		{"typed", `{"method":"sum","params":[1,2,3]}`, `{"result":{"total":6}}`},
		{"bad params", `{"method":"sum","params":"x"}`, `{"error":"json: cannot unmarshal string into Go value of type []int","class":"permanent"}`},
		{"unknown", `{"method":"nope"}`, `{"error":"unknown method \"nope\"","class":"permanent"}`},
		{"malformed", `{`, `{"error":"malformed request: unexpected end of JSON input","class":"permanent"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(call([]byte(c.req))); got != c.want {
				t.Errorf("call(%s) = %s, want %s", c.req, got, c.want)
			}
		})
	}
}

func TestCallUnmarshalableResult(t *testing.T) {
	resetHandlers(t)
	Handle("bad", func(json.RawMessage) (any, error) { return make(chan int), nil })
	var resp struct{ Error string }
	if err := json.Unmarshal(call([]byte(`{"method":"bad"}`)), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == "" {
		t.Error("want an error response when the result cannot be encoded")
	}
}

func TestHandleDuplicatePanics(t *testing.T) {
	resetHandlers(t)
	Handle("m", func(json.RawMessage) (any, error) { return nil, nil })
	defer func() {
		if recover() == nil {
			t.Error("duplicate Handle did not panic")
		}
	}()
	Handle("m", func(json.RawMessage) (any, error) { return nil, nil })
}

func TestCallEncodeFailureIsPermanent(t *testing.T) {
	resetHandlers(t)
	Handle("bad", func(json.RawMessage) (any, error) { return make(chan int), nil })
	var resp struct{ Class Class }
	if err := json.Unmarshal(call([]byte(`{"method":"bad"}`)), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Class != Permanent {
		t.Errorf("class = %q, want %q", resp.Class, Permanent)
	}
}

func TestClassForStatus(t *testing.T) {
	for status, want := range map[int]Class{
		200: Permanent, 400: Permanent, 401: Auth, 403: Auth, 404: NotFound,
		408: Transient, 422: Permanent, 429: RateLimited, 500: Transient, 503: Transient,
	} {
		if got := ClassForStatus(status); got != want {
			t.Errorf("ClassForStatus(%d) = %q, want %q", status, got, want)
		}
	}
}

func TestErrorfWrapsAndUnwraps(t *testing.T) {
	base := errors.New("base")
	err := Errorf(NotFound, "gone: %w", base)
	if !errors.Is(err, base) {
		t.Error("Errorf lost the %w cause")
	}
	var ce *Error
	if !errors.As(err, &ce) || ce.Class != NotFound || err.Error() != "gone: base" {
		t.Errorf("err = %v (%+v)", err, ce)
	}
}

func TestCallRaw(t *testing.T) {
	resetHandlers(t)
	var got []byte
	HandleRaw("raw.echo", func(p []byte) (any, error) {
		got = append([]byte(nil), p...)
		return len(p), nil
	})
	HandleRaw("raw.fail", func([]byte) (any, error) { return nil, Errorf(Transient, "try later") })
	Handle("json.echo", func(p json.RawMessage) (any, error) { return p, nil })

	cases := []struct{ name, req, want, payload string }{
		{"payload verbatim", "raw.echo\n\"a\\n\r\xff{\n", `{"result":8}`, "\"a\\n\r\xff{\n"},
		{"empty payload", "raw.echo\n", `{"result":0}`, ""},
		{"classed error", "raw.fail\nx", `{"error":"try later","class":"transient"}`, ""},
		{"unknown", "nope\nx", `{"error":"unknown method \"nope\"","class":"permanent"}`, ""},
		{"json method is not raw", "json.echo\nx", `{"error":"unknown method \"json.echo\"","class":"permanent"}`, ""},
		{"no newline", "raw.echo", `{"error":"malformed request: raw request has no method line","class":"permanent"}`, ""},
		{"json unaffected", `{"method":"json.echo","params":[1]}`, `{"result":[1]}`, ""},
		{"raw name via json", `{"method":"raw.echo"}`, `{"error":"unknown method \"raw.echo\"","class":"permanent"}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got = nil
			if resp := string(call([]byte(c.req))); resp != c.want {
				t.Errorf("call = %s, want %s", resp, c.want)
			}
			if string(got) != c.payload {
				t.Errorf("payload = %q, want %q", got, c.payload)
			}
		})
	}
}

func TestHandleRawDuplicatePanics(t *testing.T) {
	cases := []struct {
		name  string
		first func()
		again func()
	}{
		{"raw twice", func() { HandleRaw("m", nil) }, func() { HandleRaw("m", nil) }},
		{"json then raw", func() { Handle("m", nil) }, func() { HandleRaw("m", nil) }},
		{"raw then json", func() { HandleRaw("m", nil) }, func() { Handle("m", nil) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetHandlers(t)
			c.first()
			defer func() {
				if recover() == nil {
					t.Error("duplicate registration did not panic")
				}
			}()
			c.again()
		})
	}
}
