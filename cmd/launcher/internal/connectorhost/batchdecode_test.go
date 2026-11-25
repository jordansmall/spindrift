package connectorhost

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// Every request batchEnd packs must fit under maxRequest, or the host would
// refuse the very batches it builds; a line over the cap alone goes alone.
func TestBatchEndPacksRequestsUnderMaxRequest(t *testing.T) {
	t.Parallel()
	empty, err := encodeRequest(decodeBatchMethod, map[string]any{"decoder": uint32(math.MaxUint32), "lines": []string{}})
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("<&>\"", maxRequest/12) // 2.7 MiB raw; each quote escapes to two bytes
	lines := []string{big, big, "small", big, strings.Repeat("x", maxRequest+1), "tail"}
	var runs [][2]int
	for from := 0; from < len(lines); {
		end := batchEnd(lines, from, maxRequest-len(empty), (*Connector)(nil).jsonCodec())
		if end <= from {
			t.Fatalf("batchEnd(%d) = %d, made no progress", from, end)
		}
		req, err := encodeRequest(decodeBatchMethod, map[string]any{"decoder": uint32(math.MaxUint32), "lines": lines[from:end]})
		if err != nil {
			t.Fatal(err)
		}
		if len(req) > maxRequest && end-from > 1 {
			t.Errorf("lines %d..%d encode to %d bytes, over maxRequest %d", from, end, len(req), maxRequest)
		}
		runs = append(runs, [2]int{from, end})
		from = end
	}
	if len(runs) < 4 {
		t.Errorf("runs = %v, want the large lines split across several requests", runs)
	}
	if last := runs[len(runs)-1]; last != [2]int{5, 6} {
		t.Errorf("last run = %v, want the lone tail line", last)
	}
}

func TestBatchEvents(t *testing.T) {
	two := []string{"l0", "l1"}
	for name, tc := range map[string]struct {
		raw   string
		lines []string
		ok    bool
	}{
		"matching":  {`[[{"Kind":"text","Text":"a"}],[]]`, two, true},
		"short":     {`[[]]`, two, false},
		"long":      {`[[],[]]`, two[:1], false},
		"not array": {`{"a":1}`, two[:1], false},
		"null":      {`null`, two[:1], false},
	} {
		evs, ok := batchEvents(json.RawMessage(tc.raw), tc.lines)
		if ok != tc.ok || (ok && len(evs) != len(tc.lines)) {
			t.Errorf("%s: batchEvents = %v, %v; want ok=%v", name, evs, ok, tc.ok)
		}
	}
}

// One rejected element fails only its own line, as a rejected per-line
// driver.decode does; its siblings keep their events.
func TestBatchEventsRejectsOnlyTheBadElement(t *testing.T) {
	for name, raw := range map[string]string{
		"forged host_op": `[[{"Kind":"text","Text":"a"}],[{"Kind":"host_op"}]]`,
		"unknown field":  `[[{"Kind":"text","Text":"a"}],[{"nope":1}]]`,
		"not an events":  `[[{"Kind":"text","Text":"a"}],"x"]`,
	} {
		evs, ok := batchEvents(json.RawMessage(raw), []string{"l0", " l1 "})
		if !ok || len(evs) != 2 {
			t.Fatalf("%s: batchEvents = %v, %v; want 2 lines, ok", name, evs, ok)
		}
		if len(evs[0]) != 1 || evs[0][0].Kind != "text" || evs[0][0].Text != "a" {
			t.Errorf("%s: good sibling = %v, want its text event", name, evs[0])
		}
		if len(evs[1]) != 1 || evs[1][0].Kind != driverkit.KindUnparsed || evs[1][0].Text != "l1" {
			t.Errorf("%s: bad element = %v, want one unparsed event carrying the line", name, evs[1])
		}
	}
}

// The guest routes any request whose first byte is not '{' as raw, so a JSON
// request must never open with whitespace.
func TestEncodeRequestStartsWithBrace(t *testing.T) {
	for _, params := range []any{nil, map[string]any{}, "x", []string{" a"}} {
		req, err := encodeRequest("m", params)
		if err != nil {
			t.Fatal(err)
		}
		if len(req) == 0 || req[0] != '{' {
			t.Errorf("encodeRequest(%v) = %q, want a leading '{'", params, req)
		}
	}
}
