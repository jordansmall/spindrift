package chore

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	cases := []struct {
		name    string
		chores  string
		every   string
		classes string
		want    []Chore
		wantErr string // substring; "" means no error
	}{
		{
			name:    "full valid config",
			chores:  "bugs docs-drift",
			every:   "6h docs-drift=168h",
			classes: "bugs=error-handling,resource-leak refactor=dead-code docs-drift=stale-reference",
			want: []Chore{
				{Name: "bugs", Every: 6 * time.Hour, Classes: []string{"error-handling", "resource-leak"}},
				{Name: "docs-drift", Every: 168 * time.Hour, Classes: []string{"stale-reference"}},
			},
		},
		{
			name:   "no bare token defaults to DefaultEvery",
			chores: "bugs docs-drift",
			every:  "docs-drift=1h",
			want: []Chore{
				{Name: "bugs", Every: DefaultEvery},
				{Name: "docs-drift", Every: time.Hour},
			},
		},
		{
			name:   "bare 0 means no interval",
			chores: "bugs",
			every:  "0",
			want:   []Chore{{Name: "bugs", Every: 0}},
		},
		{
			name:   "empty BUTLER_CHORES",
			chores: "",
			want:   []Chore{},
		},
		{
			name:    "unknown override name",
			chores:  "bugs",
			every:   "docs-drift=1h",
			wantErr: "override for chore \"docs-drift\", which is not enabled",
		},
		{
			name:    "class-list name neither enabled nor built-in",
			chores:  "bugs",
			classes: "notachore=error-handling",
			wantErr: `chore "notachore" is not enabled and not a built-in chore`,
		},
		{
			name:    "built-in-not-enabled class entry is ignored",
			chores:  "bugs",
			classes: "refactor=dead-code",
			want:    []Chore{{Name: "bugs", Every: DefaultEvery}},
		},
		{
			name:    "duplicate overrides",
			chores:  "bugs",
			every:   "6h bugs=1h bugs=2h",
			wantErr: "duplicate override",
		},
		{
			name:    "bad duration",
			chores:  "bugs",
			every:   "bogus",
			wantErr: "invalid duration",
		},
		{
			name:    "negative duration",
			chores:  "bugs",
			every:   "-5m",
			wantErr: "negative duration",
		},
		{
			name:    "two bare defaults",
			chores:  "bugs",
			every:   "1h 2h",
			wantErr: "more than one bare default token",
		},
		{
			name:    "missing equals in classes",
			chores:  "bugs",
			classes: "bugs",
			wantErr: "missing '='",
		},
		{
			name:    "invalid class",
			chores:  "bugs",
			classes: "bugs=Invalid_Class",
			wantErr: "invalid class",
		},
		{
			name:    "invalid chore name in BUTLER_CHORES",
			chores:  "bad/name",
			wantErr: "invalid name format",
		},
		{
			name:    "several problems across knobs each reported once",
			chores:  "bugs bad/name",
			every:   "unknown=1h",
			classes: "alsobad=x",
			wantErr: "MULTI", // checked specially below
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Load(tc.chores, tc.every, tc.classes)

			if tc.wantErr == "MULTI" {
				if err == nil {
					t.Fatalf("Load(%q, %q, %q): got nil error, want multiple problems", tc.chores, tc.every, tc.classes)
				}
				msg := err.Error()
				for _, substr := range []string{`"bad/name"`, `"unknown"`, `"alsobad"`} {
					if got := strings.Count(msg, substr); got != 1 {
						t.Errorf("Load() err = %v, want substring %q exactly once, got %d", err, substr, got)
					}
				}
				return
			}

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Load(%q, %q, %q): %v", tc.chores, tc.every, tc.classes, err)
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("Load(%q, %q, %q) = %#v, want %#v", tc.chores, tc.every, tc.classes, got, tc.want)
				}
				return
			}

			if err == nil {
				t.Fatalf("Load(%q, %q, %q): got nil error, want substring %q", tc.chores, tc.every, tc.classes, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Load(%q, %q, %q) err = %v, want substring %q", tc.chores, tc.every, tc.classes, err, tc.wantErr)
			}
		})
	}
}
