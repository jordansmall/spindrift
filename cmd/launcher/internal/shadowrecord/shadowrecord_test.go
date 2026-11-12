package shadowrecord

import (
	"encoding/json"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/hostop"
)

func newRec() (*Recorder, *[]hostop.SpindriftOp) {
	var ops []hostop.SpindriftOp
	return New("github", func(op hostop.SpindriftOp) { ops = append(ops, op) }), &ops
}

func TestCheckEqualIsSilent(t *testing.T) {
	r, ops := newRec()
	r.Check("c", 1, "line", json.RawMessage(`{"a":1}`), json.RawMessage(`{"a":1}`))
	if len(*ops) != 0 || r.Sum.Diverged != 0 || r.Sum.Recorded != 0 {
		t.Errorf("ops=%d sum=%+v, want nothing", len(*ops), r.Sum)
	}
}

func TestCheckEmitsDivergence(t *testing.T) {
	r, ops := newRec()
	r.Check("events", 7, "the line", json.RawMessage(`1`), json.RawMessage(`2`))
	if len(*ops) != 1 || r.Sum.Diverged != 1 || r.Sum.Recorded != 1 {
		t.Fatalf("ops=%d sum=%+v", len(*ops), r.Sum)
	}
	op := (*ops)[0]
	d := op.Divergence
	if op.Op != hostop.OpShadowDivergence || d == nil {
		t.Fatalf("op = %+v", op)
	}
	if d.Driver != "github" || d.Check != "events" || d.LineNo != 7 || d.Line != "the line" ||
		string(d.Native) != "1" || string(d.Shadow) != "2" {
		t.Errorf("divergence = %+v", d)
	}
}

func TestCheckClipsOversizedPayloads(t *testing.T) {
	r, ops := newRec()
	big := strings.Repeat("x", 3*maxPayload)
	r.Check("c", 0, big, EncodeJSON(big), EncodeJSON("small"))
	d := (*ops)[0].Divergence
	if len(d.Line) > maxPayload+32 || !strings.HasSuffix(d.Line, "bytes]") {
		t.Errorf("line not clipped: len %d", len(d.Line))
	}
	var s string
	if err := json.Unmarshal(d.Native, &s); err != nil {
		t.Fatalf("clipped native is not valid JSON: %v", err)
	}
	if len(s) > maxPayload+32 {
		t.Errorf("native not clipped: len %d", len(s))
	}
}

func TestCheckCapsRecordedButCountsAll(t *testing.T) {
	r, ops := newRec()
	for i := 0; i < MaxRecorded+50; i++ {
		r.Check("c", i, "", json.RawMessage(`1`), json.RawMessage(`2`))
	}
	if len(*ops) != MaxRecorded || r.Sum.Recorded != MaxRecorded || r.Sum.Diverged != MaxRecorded+50 {
		t.Errorf("ops=%d recorded=%d diverged=%d", len(*ops), r.Sum.Recorded, r.Sum.Diverged)
	}
}

func TestFinishEmitsSummaryCopy(t *testing.T) {
	r, ops := newRec()
	r.Sum.Compared = 3
	r.Finish()
	r.Sum.Compared = 9
	sum := (*ops)[0].ShadowSummary
	if (*ops)[0].Op != hostop.OpShadowSummary || sum == nil || sum.Compared != 3 || sum.Driver != "github" {
		t.Errorf("summary op = %+v", (*ops)[0])
	}
}

func TestEncodeJSONUnencodable(t *testing.T) {
	got := string(EncodeJSON(make(chan int)))
	if !strings.HasPrefix(got, `"unencodable: `) {
		t.Errorf("got %s", got)
	}
}
