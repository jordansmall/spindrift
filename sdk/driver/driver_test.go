package driver

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
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
		`"terminalPatterns":[{"substr":"bad flag","reason":"unsupportedFlag"}],"writeTool":"write_file",` +
		`"classify":{"chunkOversized":false,"firstMatch":false},"totals":"perCall","result":"events"}`
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

func TestDescribeCarriesStrategyData(t *testing.T) {
	s := countingStrategy()
	s.Classify = ClassifyOptions{ChunkOversized: true, FirstMatch: true}
	s.Totals = SessionTotals
	s.Result = LastMessage
	s.DefaultTopLevelRole = "lead"
	s.Heartbeat.KindOrder = []string{"read", "edit"}
	s.Heartbeat.KindPlurals = map[string]string{"search": "searches"}
	_, m := handlers(s)
	got := marshal(t, call(t, m, "driver.describe", "null"))
	for _, want := range []string{
		`"classify":{"chunkOversized":true,"firstMatch":true}`,
		`"totals":"session"`,
		`"result":"lastMessage"`,
		`"defaultTopLevelRole":"lead"`,
		`"kindOrder":["read","edit"]`,
		`"kindPlurals":{"search":"searches"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("describe = %s\nmissing %s", got, want)
		}
	}
}

func TestDescribeZeroValuesAreDefaults(t *testing.T) {
	_, m := handlers(countingStrategy())
	got := marshal(t, call(t, m, "driver.describe", "null"))
	for _, want := range []string{
		`"classify":{"chunkOversized":false,"firstMatch":false}`, `"totals":"perCall"`, `"result":"events"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("describe = %s\nmissing %s", got, want)
		}
	}
	for _, absent := range []string{"defaultTopLevelRole", "kindOrder", "kindPlurals"} {
		if strings.Contains(got, absent) {
			t.Errorf("describe = %s\nunexpected %s", got, absent)
		}
	}
}

func TestHeartbeatCapabilities(t *testing.T) {
	s := countingStrategy()
	s.Heartbeat.Kind = func(tool string) string { return "k:" + tool }
	caps, m := handlers(s)
	if want := []string{"driver", "driver.toolKind"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	for _, name := range []string{"driver.toolPhase", "driver.modelFamily"} {
		if _, ok := m[name]; ok {
			t.Errorf("%s registered without its func", name)
		}
	}
	s.Heartbeat.Phase = func(string, json.RawMessage) string { return "" }
	s.Heartbeat.Family = func(string) (string, int) { return "", 0 }
	caps, _ = handlers(s)
	want := []string{"driver", "driver.toolPhase", "driver.toolKind", "driver.modelFamily"}
	if !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
}

func TestHeartbeatMethods(t *testing.T) {
	s := countingStrategy()
	s.Heartbeat.Phase = func(tool string, input json.RawMessage) string { return tool + ":" + string(input) }
	s.Heartbeat.Kind = func(tool string) string { return "k:" + tool }
	s.Heartbeat.Family = func(model string) (string, int) { return "f:" + model, len(model) }
	_, m := handlers(s)
	if got := marshal(t, call(t, m, "driver.toolPhase", `{"tool":"Task","input":{"a":1}}`)); got != `{"phase":"Task:{\"a\":1}"}` {
		t.Errorf("toolPhase = %s", got)
	}
	if got := marshal(t, call(t, m, "driver.toolKind", `{"tool":"Read"}`)); got != `{"kind":"k:Read"}` {
		t.Errorf("toolKind = %s", got)
	}
	if got := marshal(t, call(t, m, "driver.modelFamily", `{"model":"opus"}`)); got != `{"label":"f:opus","rank":4}` {
		t.Errorf("modelFamily = %s", got)
	}
	for _, name := range []string{"driver.toolPhase", "driver.toolKind", "driver.modelFamily"} {
		if _, err := m[name](json.RawMessage(`[`)); err == nil {
			t.Errorf("%s accepted malformed params", name)
		}
	}
}

// lineFilter holds a partial line across writes and upper-cases complete
// ones, so a test can tell stateful behaviour from a pass-through.
type lineFilter struct {
	w    io.Writer
	held []byte
	err  error
}

func (f *lineFilter) Write(p []byte) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.held = append(f.held, p...)
	for {
		i := bytes.IndexByte(f.held, '\n')
		if i < 0 {
			return len(p), nil
		}
		f.w.Write(upperASCII(f.held[:i+1]))
		f.held = f.held[i+1:]
	}
}

func (f *lineFilter) Flush() error {
	if f.err != nil {
		return f.err
	}
	f.w.Write(f.held)
	f.held = nil
	return nil
}

func filterStrategy() Strategy {
	s := countingStrategy()
	s.NewStreamFilter = func(w io.Writer) StreamFilter { return &lineFilter{w: w} }
	return s
}

func newFilter(t *testing.T, m map[string]func(json.RawMessage) (any, error)) string {
	t.Helper()
	var r struct {
		Filter uint32 `json:"filter"`
	}
	if err := json.Unmarshal([]byte(marshal(t, call(t, m, "driver.newStreamFilter", "{}"))), &r); err != nil {
		t.Fatal(err)
	}
	return strconv.Itoa(int(r.Filter))
}

func streamData(t *testing.T, m map[string]func(json.RawMessage) (any, error), method, params string) []byte {
	t.Helper()
	var r struct {
		Data []byte `json:"data"`
	}
	if err := json.Unmarshal([]byte(marshal(t, call(t, m, method, params))), &r); err != nil {
		t.Fatal(err)
	}
	return r.Data
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func TestStreamFilterCapability(t *testing.T) {
	caps, m := handlers(countingStrategy())
	for _, meth := range []string{"driver.newStreamFilter", "driver.streamWrite", "driver.streamFlush", "driver.freeStreamFilter"} {
		if _, ok := m[meth]; ok {
			t.Fatalf("%s registered without NewStreamFilter", meth)
		}
	}
	if slices.Contains(caps, "driver.streamFilter") {
		t.Fatalf("caps = %v, want no driver.streamFilter", caps)
	}
	caps, m = handlers(filterStrategy())
	if !slices.Contains(caps, "driver.streamFilter") {
		t.Fatalf("caps = %v, want driver.streamFilter", caps)
	}
	for _, meth := range []string{"driver.newStreamFilter", "driver.streamWrite", "driver.streamFlush", "driver.freeStreamFilter"} {
		if _, ok := m[meth]; !ok {
			t.Fatalf("%s missing", meth)
		}
	}
}

func TestStreamFilterRoundTrip(t *testing.T) {
	_, m := handlers(filterStrategy())
	h := newFilter(t, m)
	write := func(p []byte) []byte {
		return streamData(t, m, "driver.streamWrite", `{"filter":`+h+`,"data":"`+b64(p)+`"}`)
	}
	if got := write([]byte("ab\xffc")); len(got) != 0 {
		t.Fatalf("partial line leaked: %q", got)
	}
	if got, want := write([]byte("d\n\xfe")), []byte("AB\xffCD\n"); !bytes.Equal(got, want) {
		t.Fatalf("write = %q, want %q", got, want)
	}
	if got, want := streamData(t, m, "driver.streamFlush", `{"filter":`+h+`}`), []byte("\xfe"); !bytes.Equal(got, want) {
		t.Fatalf("flush = %q, want %q", got, want)
	}
	if got := streamData(t, m, "driver.streamFlush", `{"filter":`+h+`}`); len(got) != 0 {
		t.Fatalf("second flush = %q, want empty", got)
	}
}

func TestStreamFilterEmptyDataIsString(t *testing.T) {
	_, m := handlers(filterStrategy())
	h := newFilter(t, m)
	got := marshal(t, call(t, m, "driver.streamWrite", `{"filter":`+h+`,"data":"`+b64([]byte("x"))+`"}`))
	if got != `{"data":""}` {
		t.Fatalf("streamWrite = %s, want {\"data\":\"\"}", got)
	}
}

func TestStreamFilterStatePerHandle(t *testing.T) {
	_, m := handlers(filterStrategy())
	a, b := newFilter(t, m), newFilter(t, m)
	streamData(t, m, "driver.streamWrite", `{"filter":`+a+`,"data":"`+b64([]byte("held"))+`"}`)
	if got := streamData(t, m, "driver.streamFlush", `{"filter":`+b+`}`); len(got) != 0 {
		t.Fatalf("filter b flushed a's bytes: %q", got)
	}
}

func TestStreamFilterUnknownHandle(t *testing.T) {
	_, m := handlers(filterStrategy())
	for meth, params := range map[string]string{
		"driver.streamWrite": `{"filter":99,"data":""}`,
		"driver.streamFlush": `{"filter":99}`,
	} {
		if _, err := m[meth](json.RawMessage(params)); err == nil || !strings.Contains(err.Error(), "unknown filter 99") {
			t.Fatalf("%s err = %v, want unknown filter 99", meth, err)
		}
	}
}

func TestFreeStreamFilter(t *testing.T) {
	_, m := handlers(filterStrategy())
	h := newFilter(t, m)
	if res := call(t, m, "driver.freeStreamFilter", `{"filter":`+h+`}`); res != nil {
		t.Fatalf("freeStreamFilter result = %v, want nil", res)
	}
	if _, err := m["driver.streamWrite"](json.RawMessage(`{"filter":` + h + `,"data":""}`)); err == nil {
		t.Fatal("streamWrite after free should error")
	}
	call(t, m, "driver.freeStreamFilter", `{"filter":`+h+`}`) // idempotent
}

func TestStreamFilterErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	s := countingStrategy()
	s.NewStreamFilter = func(w io.Writer) StreamFilter { return &lineFilter{w: w, err: boom} }
	_, m := handlers(s)
	h := newFilter(t, m)
	if _, err := m["driver.streamWrite"](json.RawMessage(`{"filter":` + h + `,"data":"eA=="}`)); !errors.Is(err, boom) {
		t.Fatalf("streamWrite err = %v, want boom", err)
	}
	if _, err := m["driver.streamFlush"](json.RawMessage(`{"filter":` + h + `}`)); !errors.Is(err, boom) {
		t.Fatalf("streamFlush err = %v, want boom", err)
	}
}

// failAfterWriting writes to w and then fails, as a filter that errors midway.
type failAfterWriting struct {
	w    io.Writer
	fail bool
}

func (f *failAfterWriting) Write(p []byte) (int, error) {
	f.w.Write([]byte("junk"))
	if f.fail {
		return 0, errors.New("boom")
	}
	return len(p), nil
}

func (f *failAfterWriting) Flush() error { return nil }

func TestStreamFilterFailedCallLeavesNoOutputBehind(t *testing.T) {
	var f *failAfterWriting
	s := countingStrategy()
	s.NewStreamFilter = func(w io.Writer) StreamFilter { f = &failAfterWriting{w: w}; return f }
	_, m := handlers(s)
	h := newFilter(t, m)
	f.fail = true
	if _, err := m["driver.streamWrite"](json.RawMessage(`{"filter":` + h + `,"data":"eA=="}`)); err == nil {
		t.Fatal("streamWrite succeeded, want an error")
	}
	f.fail = false
	want := `{"data":"` + b64([]byte("junk")) + `"}`
	if got := marshal(t, call(t, m, "driver.streamWrite", `{"filter":`+h+`,"data":"eA=="}`)); got != want {
		t.Fatalf("streamWrite after a failed call = %s, want %s", got, want)
	}
}

// upperASCII leaves non-ASCII bytes alone; bytes.ToUpper would rewrite
// invalid UTF-8 to U+FFFD and hide a byte-exactness bug.
func upperASCII(p []byte) []byte {
	out := make([]byte, len(p))
	for i, c := range p {
		if 'a' <= c && c <= 'z' {
			c -= 'a' - 'A'
		}
		out[i] = c
	}
	return out
}
