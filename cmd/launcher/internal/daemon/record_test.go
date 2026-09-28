package daemon

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/report"
)

func TestParseRecord(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		kind    Kind
		want    Record
		wantOK  bool
		wantErr bool
	}{
		{
			name:   "box",
			line:   `{"event":"box","issue":"42","phase":"initial"}`,
			kind:   KindOf(dispatchkind.Work),
			want:   Record{Event: report.EventBox, Key: dispatchkey.Issue("42"), Phase: "initial"},
			wantOK: true,
		},
		{
			name:   "box fix pass",
			line:   `{"event":"box","issue":"42","phase":"fix-pass-2"}`,
			kind:   KindOf(dispatchkind.Work),
			want:   Record{Event: report.EventBox, Key: dispatchkey.Issue("42"), Phase: "fix-pass-2"},
			wantOK: true,
		},
		{
			name:   "settled",
			line:   `{"event":"settled","issue":"42","state":"complete","note":"merged"}`,
			kind:   KindOf(dispatchkind.Work),
			want:   Record{Event: report.EventSettled, Key: dispatchkey.Issue("42"), State: "complete", Note: "merged"},
			wantOK: true,
		},
		{
			name: "unknown event ignored",
			line: `{"event":"heartbeat","issue":"42"}`,
		},
		{
			name: "blank line ignored",
			line: "",
		},
		{
			name:    "malformed json",
			line:    `{"event":"box"`,
			kind:    KindOf(dispatchkind.Work),
			wantErr: true,
		},
		{
			name:    "empty issue",
			line:    `{"event":"box","issue":""}`,
			kind:    KindOf(dispatchkind.Work),
			wantErr: true,
		},
		{
			name:    "non-digit issue",
			line:    `{"event":"box","issue":"42a"}`,
			kind:    KindOf(dispatchkind.Work),
			wantErr: true,
		},
		{
			name:    "signed issue",
			line:    `{"event":"box","issue":"-42"}`,
			kind:    KindOf(dispatchkind.Work),
			wantErr: true,
		},
		{
			name:    "issue too long",
			line:    `{"event":"box","issue":"12345678901"}`,
			kind:    KindOf(dispatchkind.Work),
			wantErr: true,
		},
		{
			name:   "issue at length cap",
			line:   `{"event":"box","issue":"1234567890"}`,
			kind:   KindOf(dispatchkind.Work),
			want:   Record{Event: report.EventBox, Key: dispatchkey.Issue("1234567890")},
			wantOK: true,
		},
		{
			name:   "chore box",
			line:   `{"event":"box","chore":"bugs","phase":"initial"}`,
			kind:   KindOf(dispatchkind.Butler),
			want:   Record{Event: report.EventBox, Key: dispatchkey.Chore("bugs"), Phase: "initial"},
			wantOK: true,
		},
		{
			name:   "chore settled",
			line:   `{"event":"settled","chore":"bugs","state":"complete","note":"2 filed"}`,
			kind:   KindOf(dispatchkind.Butler),
			want:   Record{Event: report.EventSettled, Key: dispatchkey.Chore("bugs"), State: "complete", Note: "2 filed"},
			wantOK: true,
		},
		{
			name:    "empty chore",
			line:    `{"event":"box","chore":""}`,
			kind:    KindOf(dispatchkind.Butler),
			wantErr: true,
		},
		{
			name:    "chore too long",
			line:    `{"event":"box","chore":"` + strings.Repeat("a", 65) + `"}`,
			kind:    KindOf(dispatchkind.Butler),
			wantErr: true,
		},
		{
			name:   "chore at length cap",
			line:   `{"event":"box","chore":"` + strings.Repeat("a", 64) + `"}`,
			kind:   KindOf(dispatchkind.Butler),
			want:   Record{Event: report.EventBox, Key: dispatchkey.Chore(strings.Repeat("a", 64))},
			wantOK: true,
		},
		{
			name:    "invalid chore charset",
			line:    `{"event":"box","chore":"bugs/x"}`,
			kind:    KindOf(dispatchkind.Butler),
			wantErr: true,
		},
		{
			name:    "both issue and chore",
			line:    `{"event":"box","issue":"42","chore":"bugs"}`,
			kind:    KindOf(dispatchkind.Work),
			wantErr: true,
		},
		{
			name:    "neither issue nor chore",
			line:    `{"event":"box"}`,
			kind:    KindOf(dispatchkind.Work),
			wantErr: true,
		},
		{
			name:    "issue record from butler kind",
			line:    `{"event":"box","issue":"42","phase":"initial"}`,
			kind:    KindOf(dispatchkind.Butler),
			wantErr: true,
		},
		{
			name:    "chore record from non-butler kind",
			line:    `{"event":"box","chore":"bugs","phase":"initial"}`,
			kind:    KindOf(dispatchkind.Work),
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := ParseRecord(tc.line, tc.kind)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseRecord(%q) err = %v, wantErr %v", tc.line, err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if ok != tc.wantOK {
				t.Fatalf("ParseRecord(%q) ok = %v, want %v", tc.line, ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Fatalf("ParseRecord(%q) = %+v, want %+v", tc.line, got, tc.want)
			}
		})
	}
}

// TestParseRecord_KindMismatchIsDistinct pins that only a key/kind mismatch
// wraps ErrKindMismatch, so readReports can report it apart from a
// malformed line.
func TestParseRecord_KindMismatchIsDistinct(t *testing.T) {
	cases := []struct {
		line     string
		kind     Kind
		mismatch bool
	}{
		{`{"event":"box","issue":"42"}`, KindOf(dispatchkind.Butler), true},
		{`{"event":"box","chore":"bugs"}`, KindOf(dispatchkind.Research), true},
		{`{"event":"box","chore":"bugs/x"}`, KindOf(dispatchkind.Butler), false},
		{`not json`, KindOf(dispatchkind.Work), false},
	}
	for _, tc := range cases {
		_, _, err := ParseRecord(tc.line, tc.kind)
		if err == nil {
			t.Fatalf("ParseRecord(%q, %s) err = nil, want an error", tc.line, tc.kind)
		}
		if got := errors.Is(err, ErrKindMismatch); got != tc.mismatch {
			t.Errorf("ParseRecord(%q, %s) errors.Is(ErrKindMismatch) = %v, want %v (err: %v)", tc.line, tc.kind, got, tc.mismatch, err)
		}
	}
}
