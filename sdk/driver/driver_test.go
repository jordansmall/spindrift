package driver

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/sdk/event"
)

func countingStrategy() Strategy {
	return Strategy{
		Name: "fake",
		NewDecoder: func() Decoder {
			n := 0
			return func(line string) []event.Event {
				n++
				return []event.Event{{Kind: event.KindText, Text: line + ":" + strconv.Itoa(n)}}
			}
		},
		TransientPatterns: []Pattern{{Substr: "429", Reason: RateLimit}},
		TerminalPatterns:  []Pattern{{Substr: "bad flag", Reason: UnsupportedFlag}},
		WriteTool:         "write_file",
		ResultEvent:       func(text string) ([]byte, error) { return []byte("R:" + text), nil },
		SessionFlags: func(mode, repoSlug, issue, home string) string {
			return strings.Join([]string{mode, repoSlug, issue, home}, "|")
		},
	}
}

func call(t *testing.T, m map[string]func(json.RawMessage) (any, error), method, params string) any {
	t.Helper()
	res, err := m[method](json.RawMessage(params))
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return res
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCapabilities(t *testing.T) {
	s := countingStrategy()
	caps, methods := handlers(s)
	if want := []string{"driver"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	if _, ok := methods["driver.resolveExit"]; ok {
		t.Fatal("resolveExit registered without ResolveExit")
	}
	s.ResolveExit = func([]byte, int) (int, error) { return 0, nil }
	caps, methods = handlers(s)
	if want := []string{"driver", "driver.resolveExit"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	if _, ok := methods["driver.resolveExit"]; !ok {
		t.Fatal("resolveExit missing")
	}
}

func TestDescribe(t *testing.T) {
	_, m := handlers(countingStrategy())
	got := marshal(t, call(t, m, "driver.describe", "null"))
	want := `{"name":"fake","transientPatterns":[{"substr":"429","reason":"rateLimit"}],` +
		`"terminalPatterns":[{"substr":"bad flag","reason":"unsupportedFlag"}],"writeTool":"write_file"}`
	if got != want {
		t.Fatalf("describe = %s\nwant %s", got, want)
	}
	s := countingStrategy()
	s.TransientPatterns, s.TerminalPatterns = nil, nil
	_, m = handlers(s)
	got = marshal(t, call(t, m, "driver.describe", "null"))
	if !strings.Contains(got, `"transientPatterns":[]`) || !strings.Contains(got, `"terminalPatterns":[]`) {
		t.Fatalf("empty patterns not []: %s", got)
	}
}

func newHandle(t *testing.T, m map[string]func(json.RawMessage) (any, error)) uint32 {
	t.Helper()
	var r struct {
		Decoder uint32 `json:"decoder"`
	}
	if err := json.Unmarshal([]byte(marshal(t, call(t, m, "driver.newDecoder", "null"))), &r); err != nil {
		t.Fatal(err)
	}
	return r.Decoder
}

func TestDecoderStatePerHandle(t *testing.T) {
	_, m := handlers(countingStrategy())
	a, b := newHandle(t, m), newHandle(t, m)
	if a == b {
		t.Fatalf("handles collide: %d", a)
	}
	dec := func(h uint32, line string) string {
		return marshal(t, call(t, m, "driver.decode", `{"decoder":`+strconv.Itoa(int(h))+`,"line":"`+line+`"}`))
	}
	if got := dec(a, "x"); !strings.Contains(got, `"Text":"x:1"`) {
		t.Fatalf("a first: %s", got)
	}
	if got := dec(a, "x"); !strings.Contains(got, `"Text":"x:2"`) {
		t.Fatalf("a second: %s", got)
	}
	if got := dec(b, "y"); !strings.Contains(got, `"Text":"y:1"`) {
		t.Fatalf("b first: %s", got)
	}
}

func TestDecodeNilIsEmptyArray(t *testing.T) {
	s := countingStrategy()
	s.NewDecoder = func() Decoder { return func(string) []event.Event { return nil } }
	_, m := handlers(s)
	h := newHandle(t, m)
	got := marshal(t, call(t, m, "driver.decode", `{"decoder":`+strconv.Itoa(int(h))+`,"line":"z"}`))
	if got != "[]" {
		t.Fatalf("decode = %s, want []", got)
	}
}

func TestDecodeUnknownHandle(t *testing.T) {
	_, m := handlers(countingStrategy())
	if _, err := m["driver.decode"](json.RawMessage(`{"decoder":99,"line":"x"}`)); err == nil {
		t.Fatal("want error for unknown handle")
	}
}

func TestFreeDecoder(t *testing.T) {
	_, m := handlers(countingStrategy())
	h := strconv.Itoa(int(newHandle(t, m)))
	if res := call(t, m, "driver.freeDecoder", `{"decoder":`+h+`}`); res != nil {
		t.Fatalf("freeDecoder result = %v, want nil", res)
	}
	if _, err := m["driver.decode"](json.RawMessage(`{"decoder":` + h + `,"line":"x"}`)); err == nil {
		t.Fatal("decode after free should error")
	}
	call(t, m, "driver.freeDecoder", `{"decoder":`+h+`}`) // idempotent
}

func TestResultEvent(t *testing.T) {
	_, m := handlers(countingStrategy())
	if got := marshal(t, call(t, m, "driver.resultEvent", `{"text":"hi"}`)); got != `{"event":"R:hi"}` {
		t.Fatalf("resultEvent = %s", got)
	}
	s := countingStrategy()
	s.ResultEvent = func(string) ([]byte, error) { return nil, errors.New("boom") }
	_, m = handlers(s)
	if _, err := m["driver.resultEvent"](json.RawMessage(`{"text":"hi"}`)); err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestSessionFlags(t *testing.T) {
	_, m := handlers(countingStrategy())
	got := marshal(t, call(t, m, "driver.sessionFlags", `{"mode":"m","repoSlug":"o/r","issue":"7","home":"/h"}`))
	if got != `{"flags":"m|o/r|7|/h"}` {
		t.Fatalf("sessionFlags = %s", got)
	}
}

func TestResolveExit(t *testing.T) {
	s := countingStrategy()
	s.ResolveExit = func(log []byte, code int) (int, error) {
		if string(log) == "fail" {
			return 0, errors.New("nope")
		}
		return code + len(log), nil
	}
	_, m := handlers(s)
	if got := marshal(t, call(t, m, "driver.resolveExit", `{"log":"abc","exitCode":1}`)); got != `{"exitCode":4}` {
		t.Fatalf("resolveExit = %s", got)
	}
	if _, err := m["driver.resolveExit"](json.RawMessage(`{"log":"fail","exitCode":1}`)); err == nil {
		t.Fatal("want error")
	}
}

func TestRequiredFields(t *testing.T) {
	for name, mutate := range map[string]func(*Strategy){
		"Name":         func(s *Strategy) { s.Name = "" },
		"NewDecoder":   func(s *Strategy) { s.NewDecoder = nil },
		"ResultEvent":  func(s *Strategy) { s.ResultEvent = nil },
		"SessionFlags": func(s *Strategy) { s.SessionFlags = nil },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("want panic")
				}
			}()
			s := countingStrategy()
			mutate(&s)
			Register(s)
		})
	}
}
