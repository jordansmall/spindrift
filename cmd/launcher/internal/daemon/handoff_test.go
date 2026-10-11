package daemon

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
)

func fullHandoff() Handoff {
	t0 := time.Date(2026, 10, 11, 12, 0, 0, 123456789, time.UTC)
	return Handoff{
		LockFD: 3,
		Stop:   true,
		Abort:  true,
		Slots: []HandoffSlot{
			{
				Slot: 0, Kind: "work", Revision: "abc123",
				ChildStart: "2026-10-11T12:00:00Z", ChildStartN: 2,
				ChildLog: ".spindrift/child-0.log", GateGen: 4,
				Key:    dispatchkey.Issue("42"),
				Issues: []string{"42", "43"},
				Pass:   "fix-pass-1", Model: "claude-x", ModelRole: "fixer",
				Child: ChildState{PID: 4242, ReportFD: 7, Partial: []byte("{\"event\":\"caf\xc3"), Discarding: true},
			},
			{
				Slot: 3, Kind: "butler", Revision: "def456",
				Key:   dispatchkey.Chore("bugs"),
				Child: ChildState{PID: 99, ReportFD: 9},
			},
			// No box record yet, so no key.
			{Slot: 4, Kind: "work", Revision: "def456", Child: ChildState{PID: 100, ReportFD: 10}},
		},
		Kinds: []HandoffKind{
			{
				Kind: "work", BackoffCur: 2 * time.Minute, Until: t0.Add(time.Minute),
				Jammed: true, Gen: 5,
				DueAt: t0.Add(time.Hour), DueOnTipMove: true,
				LimitedUntil: t0.Add(30 * time.Second),
			},
			{Kind: "research", BackoffCur: time.Second},
		},
		Breaker: []time.Time{t0, t0.Add(time.Second)},
	}
}

func TestHandoffRoundTrip(t *testing.T) {
	want := fullHandoff()
	var buf bytes.Buffer
	if err := EncodeHandoff(&buf, want); err != nil {
		t.Fatalf("EncodeHandoff: %v", err)
	}
	got, err := DecodeHandoff(&buf)
	if err != nil {
		t.Fatalf("DecodeHandoff: %v", err)
	}
	want.V = HandoffVersion
	if !reflect.DeepEqual(want, got) {
		t.Errorf("round trip mismatch\nwant %+v\n got %+v", want, got)
	}
}

func TestHandoffKeysTravelAsIssueAndChoreFields(t *testing.T) {
	var buf bytes.Buffer
	if err := EncodeHandoff(&buf, fullHandoff()); err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	for _, frag := range []string{`"issue":"42"`, `"chore":"bugs"`, `"v":1`} {
		if !strings.Contains(s, frag) {
			t.Errorf("document missing %s: %s", frag, s)
		}
	}
}

func TestHandoffEncodeStampsVersion(t *testing.T) {
	var buf bytes.Buffer
	if err := EncodeHandoff(&buf, Handoff{V: 99}); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeHandoff(&buf)
	if err != nil {
		t.Fatalf("DecodeHandoff: %v", err)
	}
	if got.V != HandoffVersion {
		t.Errorf("V = %d, want %d", got.V, HandoffVersion)
	}
}

func TestHandoffDecodeRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		doc  string
		want string
	}{
		"future version":     {`{"v":2,"lock_fd":3,"newthing":[1]}`, "version 2"},
		"missing version":    {`{"lock_fd":3}`, "version 0"},
		"malformed":          {`{"v":1,`, "decode"},
		"unknown field":      {`{"v":1,"lock_fd":3,"bogus":true}`, "bogus"},
		"both key fields":    {`{"v":1,"lock_fd":3,"slots":[{"slot":0,"kind":"work","issue":"1","chore":"x"}]}`, "slot"},
		"trailing data":      {`{"v":1,"lock_fd":3}{"v":1}`, "trailing"},
		"garbage after":      {`{"v":1,"lock_fd":3} }`, "after the document"},
		"negative lock fd":   {`{"v":1,"lock_fd":-1}`, "lock_fd"},
		"zero pid":           {`{"v":1,"lock_fd":3,"slots":[{"slot":2,"kind":"work","child":{"pid":0,"report_fd":5}}]}`, "slot 2"},
		"negative pid":       {`{"v":1,"lock_fd":3,"slots":[{"slot":6,"kind":"work","child":{"pid":-4,"report_fd":5}}]}`, "slot 6"},
		"duplicate pid":      {`{"v":1,"lock_fd":3,"slots":[{"slot":1,"kind":"work","child":{"pid":4,"report_fd":5}},{"slot":2,"kind":"work","child":{"pid":4,"report_fd":6}}]}`, "slot 2"},
		"negative report fd": {`{"v":1,"lock_fd":3,"slots":[{"slot":1,"kind":"work","child":{"pid":4,"report_fd":-1}}]}`, "slot 1"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeHandoff(strings.NewReader(tc.doc))
			if err == nil {
				t.Fatal("DecodeHandoff accepted the document")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestHandoffVersionErrorNamesSupported(t *testing.T) {
	_, err := DecodeHandoff(strings.NewReader(`{"v":7}`))
	if err == nil || !strings.Contains(err.Error(), "supports 1") {
		t.Errorf("error %v should name the supported version", err)
	}
}
