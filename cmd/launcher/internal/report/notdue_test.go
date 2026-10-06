package report

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
)

func TestNotDue_WireShape(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()
	t.Cleanup(func() { w.Close() })
	rep := FromEnv(getenvFor(map[string]string{"SPINDRIFT_REPORT_FD": fmt.Sprint(int(w.Fd()))}), os.Stderr)
	if rep == nil {
		t.Fatal("FromEnv returned nil")
	}

	// A zone-carrying instant with nanoseconds: the wire must be UTC and keep
	// the nanosecond a live claim's lift instant depends on.
	loc := time.FixedZone("X", -5*3600)
	at := time.Date(2026, 1, 1, 10, 0, 0, 1, loc)
	rep.NotDue(dispatchkey.Chore("bugs"), NextDue{At: at})
	rep.NotDue(dispatchkey.Chore("docs"), NextDue{OnTipMove: true})
	w.Close()

	var lines []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	want := []string{
		`{"event":"not_due","chore":"bugs","next_due":"2026-01-01T15:00:00.000000001Z"}`,
		`{"event":"not_due","chore":"docs","next_due":"on_tip_move"}`,
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(want), lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %s, want %s", i, lines[i], want[i])
		}
	}

	var rec Record
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantRec := Record{Event: EventNotDue, Key: dispatchkey.Chore("bugs"), NextDue: NextDue{At: time.Date(2026, 1, 1, 15, 0, 0, 1, time.UTC)}}
	if rec != wantRec {
		t.Errorf("round trip = %+v, want %+v", rec, wantRec)
	}
}

func TestNotDue_NilReporterIsSafe(t *testing.T) {
	var rep *Reporter
	rep.NotDue(dispatchkey.Chore("bugs"), NextDue{At: time.Now()})
}

func TestParseNextDue(t *testing.T) {
	at := time.Date(2026, 1, 1, 15, 0, 0, 1, time.UTC)
	cases := []struct {
		name      string
		in        string
		wantAt    time.Time
		wantOnTip bool
		wantErr   bool
	}{
		{name: "on tip move", in: NextDueOnTipMove, wantOnTip: true},
		{name: "instant with nanoseconds", in: "2026-01-01T15:00:00.000000001Z", wantAt: at},
		{name: "offset instant", in: "2026-01-01T10:00:00.000000001-05:00", wantAt: at},
		{name: "empty", in: "", wantErr: true},
		{name: "garbage", in: "tomorrow", wantErr: true},
		{name: "date only", in: "2026-01-01", wantErr: true},
		{name: "zero instant", in: "0001-01-01T00:00:00Z", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseNextDue(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got.OnTipMove != tc.wantOnTip || !got.At.Equal(tc.wantAt) {
				t.Errorf("ParseNextDue(%q) = (%v, %v), want (%v, %v)", tc.in, got.At, got.OnTipMove, tc.wantAt, tc.wantOnTip)
			}
		})
	}
}

func TestNotDue_ZeroIsNotWritten(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()
	t.Cleanup(func() { w.Close() })
	rep := FromEnv(getenvFor(map[string]string{"SPINDRIFT_REPORT_FD": fmt.Sprint(int(w.Fd()))}), os.Stderr)
	rep.NotDue(dispatchkey.Chore("bugs"), NextDue{})
	w.Close()
	if sc := bufio.NewScanner(r); sc.Scan() {
		t.Errorf("zero NextDue wrote %q, want nothing", sc.Text())
	}
}
