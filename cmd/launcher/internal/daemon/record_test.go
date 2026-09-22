package daemon

import (
	"testing"

	"spindrift.dev/launcher/internal/report"
)

func TestParseRecord(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		want    Record
		wantOK  bool
		wantErr bool
	}{
		{
			name:   "box",
			line:   `{"event":"box","issue":"42","phase":"initial"}`,
			want:   Record{Event: report.EventBox, Issue: "42", Phase: "initial"},
			wantOK: true,
		},
		{
			name:   "box fix pass",
			line:   `{"event":"box","issue":"42","phase":"fix-pass-2"}`,
			want:   Record{Event: report.EventBox, Issue: "42", Phase: "fix-pass-2"},
			wantOK: true,
		},
		{
			name:   "settled",
			line:   `{"event":"settled","issue":"42","state":"complete","note":"merged"}`,
			want:   Record{Event: report.EventSettled, Issue: "42", State: "complete", Note: "merged"},
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
			wantErr: true,
		},
		{
			name:    "empty issue",
			line:    `{"event":"box","issue":""}`,
			wantErr: true,
		},
		{
			name:    "non-digit issue",
			line:    `{"event":"box","issue":"42a"}`,
			wantErr: true,
		},
		{
			name:    "signed issue",
			line:    `{"event":"box","issue":"-42"}`,
			wantErr: true,
		},
		{
			name:    "issue too long",
			line:    `{"event":"box","issue":"12345678901"}`,
			wantErr: true,
		},
		{
			name:   "issue at length cap",
			line:   `{"event":"box","issue":"1234567890"}`,
			want:   Record{Event: report.EventBox, Issue: "1234567890"},
			wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := ParseRecord(tc.line)
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
