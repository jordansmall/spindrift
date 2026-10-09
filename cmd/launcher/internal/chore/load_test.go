package chore

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	cases := []struct {
		name             string
		chores           string
		every            string
		classes          string
		patchClasses     string
		maxPatchesPerDay int
		want             []Chore
		wantErr          string // substring; "" means no error
		// wantErrEach, when set, asserts the error is non-nil and each of
		// these substrings appears in it exactly once (used for cases where
		// a problem must be deduplicated across repeated or multiple inputs).
		wantErrEach []string
	}{
		{
			name:    "full valid config",
			chores:  "bugs docs-drift",
			every:   "6h docs-drift=168h",
			classes: "bugs=error-handling,resource-leak refactor=dead-code docs-drift=stale-reference",
			want: []Chore{
				{Name: "bugs", Every: 6 * time.Hour, PromotionClasses: []string{"error-handling", "resource-leak"}},
				{Name: "docs-drift", Every: 168 * time.Hour, PromotionClasses: []string{"stale-reference"}},
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
			name:   "records-scoped chore takes its catalog interval over the bare token",
			chores: "bugs tuning",
			every:  "3h",
			want: []Chore{
				{Name: "bugs", Every: 3 * time.Hour},
				{Name: "tuning", Every: 24 * time.Hour, Records: true, FindingLabel: "agent-tuning-finding"},
			},
		},
		{
			name:   "records-scoped chore takes its catalog interval over DefaultEvery",
			chores: "tuning",
			want:   []Chore{{Name: "tuning", Every: 24 * time.Hour, Records: true, FindingLabel: "agent-tuning-finding"}},
		},
		{
			name:   "BUTLER_EVERY override beats the catalog interval",
			chores: "tuning",
			every:  "3h tuning=48h",
			want:   []Chore{{Name: "tuning", Every: 48 * time.Hour, Records: true, FindingLabel: "agent-tuning-finding"}},
		},
		{
			name:   "override of 0 beats the catalog interval",
			chores: "tuning",
			every:  "tuning=0",
			want:   []Chore{{Name: "tuning", Every: 0, Records: true, FindingLabel: "agent-tuning-finding"}},
		},
		{
			name:    "BUTLER_CHORE_CLASSES entry for a records-scoped chore is rejected",
			chores:  "tuning",
			classes: "tuning=cost-waste",
			wantErr: `BUTLER_CHORE_CLASSES: chore "tuning" is records-scoped and never promotes (ADR 0062)`,
		},
		{
			name:    "BUTLER_CHORE_CLASSES entry for a records-scoped chore is rejected though not enabled",
			chores:  "bugs",
			classes: "tuning=cost-waste",
			wantErr: `BUTLER_CHORE_CLASSES: chore "tuning" is records-scoped`,
		},
		{
			name:             "BUTLER_PATCH_CLASSES entry for a records-scoped chore is rejected",
			chores:           "tuning",
			patchClasses:     "tuning=cost-waste",
			maxPatchesPerDay: 1,
			wantErr:          `BUTLER_PATCH_CLASSES: chore "tuning" is records-scoped and never patches (ADR 0062)`,
		},
		{
			name:   "empty BUTLER_CHORES",
			chores: "",
			want:   nil,
		},
		{
			name:    "empty BUTLER_CHORES ignores a stale override",
			chores:  "",
			every:   "6h docs-drift=168h",
			classes: "",
			want:    nil,
		},
		{
			name:   "empty BUTLER_CHORES ignores a malformed BUTLER_EVERY",
			chores: "",
			every:  "not-a-duration",
			want:   nil,
		},
		{
			name:    "empty BUTLER_CHORES ignores a non-built-in classes entry",
			chores:  "",
			classes: "notachore=error-handling",
			want:    nil,
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
			name:    "leading-dash chore name in BUTLER_CHORES",
			chores:  "-legacy",
			wantErr: "invalid name format",
		},
		{
			name:        "repeated invalid chore name reported once",
			chores:      "bad/name bad/name",
			wantErrEach: []string{"bad/name"},
		},
		{
			name:    "duplicate chore name in BUTLER_CHORES",
			chores:  "bugs docs-drift bugs",
			wantErr: `BUTLER_CHORES: duplicate chore "bugs"`,
		},
		{
			name:        "chore name repeated thrice reported once",
			chores:      "bugs bugs bugs",
			wantErrEach: []string{`duplicate chore "bugs"`},
		},
		{
			name:    "empty chore name in an override token",
			chores:  "bugs",
			every:   "=1h",
			wantErr: "empty chore name",
		},
		{
			name:    "negative override for an enabled chore",
			chores:  "docs-drift",
			every:   "docs-drift=-1h",
			wantErr: "negative duration",
		},
		{
			name:   "BUTLER_CHORES accepts tab and newline separators",
			chores: "bugs\tdocs-drift\nrefactor",
			want: []Chore{
				{Name: "bugs", Every: DefaultEvery},
				{Name: "docs-drift", Every: DefaultEvery},
				{Name: "refactor", Every: DefaultEvery},
			},
		},
		{
			name:        "several problems across knobs each reported once",
			chores:      "bugs bad/name",
			every:       "unknown=1h",
			classes:     "alsobad=x",
			wantErrEach: []string{`"bad/name"`, `"unknown"`, `"alsobad"`},
		},
		{
			name:         "rung off ignores a malformed BUTLER_PATCH_CLASSES",
			chores:       "bugs",
			patchClasses: "bugs", // missing '='
			want:         []Chore{{Name: "bugs", Every: DefaultEvery}},
		},
		{
			name:         "rung off ignores a disabled-chore BUTLER_PATCH_CLASSES entry",
			chores:       "bugs",
			patchClasses: "docs-drift=stale-reference",
			want:         []Chore{{Name: "bugs", Every: DefaultEvery}},
		},
		{
			name:             "rung on resolves PatchClasses",
			chores:           "bugs docs-drift",
			classes:          "bugs=error-handling,resource-leak docs-drift=stale-reference",
			patchClasses:     "bugs=error-handling docs-drift=stale-reference",
			maxPatchesPerDay: 1,
			want: []Chore{
				{Name: "bugs", Every: DefaultEvery, PromotionClasses: []string{"error-handling", "resource-leak"}, PatchClasses: []string{"error-handling"}},
				{Name: "docs-drift", Every: DefaultEvery, PromotionClasses: []string{"stale-reference"}, PatchClasses: []string{"stale-reference"}},
			},
		},
		{
			name:             "rung on: chore with no BUTLER_PATCH_CLASSES entry has a nil allow-list",
			chores:           "bugs",
			classes:          "bugs=error-handling",
			maxPatchesPerDay: 1,
			want:             []Chore{{Name: "bugs", Every: DefaultEvery, PromotionClasses: []string{"error-handling"}}},
		},
		{
			name:             "rung on: patch class outside the promotion classes rejected",
			chores:           "bugs",
			classes:          "bugs=error-handling",
			patchClasses:     "bugs=resource-leak",
			maxPatchesPerDay: 1,
			wantErr:          `chore "bugs": class "resource-leak" is not on its BUTLER_CHORE_CLASSES allow-list`,
		},
		{
			name:             "rung on: patch class entry for a not-enabled chore rejected",
			chores:           "bugs",
			classes:          "bugs=error-handling",
			patchClasses:     "docs-drift=stale-reference",
			maxPatchesPerDay: 1,
			wantErr:          `chore "docs-drift" is not enabled (BUTLER_CHORES="bugs")`,
		},
		{
			name:             "rung on: BUTLER_PATCH_CLASSES grammar error prefixed",
			chores:           "bugs",
			patchClasses:     "bugs",
			maxPatchesPerDay: 1,
			wantErr:          "BUTLER_PATCH_CLASSES: invalid entry",
		},
		{
			name:             "rung on: subset check skipped when BUTLER_CHORE_CLASSES itself fails to parse",
			chores:           "bugs",
			classes:          "bugs",
			patchClasses:     "bugs=resource-leak",
			maxPatchesPerDay: 1,
			wantErrEach:      []string{"BUTLER_CHORE_CLASSES: invalid entry"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Load(Knobs{Chores: tc.chores, Every: tc.every, Classes: tc.classes, PatchClasses: tc.patchClasses, MaxPatchesPerDay: tc.maxPatchesPerDay})

			if tc.wantErrEach != nil {
				if err == nil {
					t.Fatalf("Load(%q, %q, %q): got nil error, want %v each exactly once", tc.chores, tc.every, tc.classes, tc.wantErrEach)
				}
				if got != nil {
					t.Errorf("Load(%q, %q, %q) = %#v on error, want nil", tc.chores, tc.every, tc.classes, got)
				}
				msg := err.Error()
				for _, substr := range tc.wantErrEach {
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
				if !reflect.DeepEqual(withoutClassList(got), tc.want) {
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
			if got != nil {
				t.Errorf("Load(%q, %q, %q) = %#v on error, want nil", tc.chores, tc.every, tc.classes, got)
			}
		})
	}
}

func TestLoadClassList(t *testing.T) {
	cases := []struct {
		name    string
		chores  string
		classes string
		want    map[string][]string
	}{
		{
			name:   "built-in gets the catalog list",
			chores: "bugs refactor",
			want: map[string][]string{
				"bugs":     builtinClassLists["bugs"],
				"refactor": builtinClassLists["refactor"],
			},
		},
		{
			name:    "configured extra class is appended in configured order, skipping catalog classes",
			chores:  "refactor",
			classes: "refactor=dead-code,naming,duplication,layering",
			want: map[string][]string{
				"refactor": append(append([]string{}, builtinClassLists["refactor"]...), "naming", "layering"),
			},
		},
		{
			name:    "Consumer-declared chore stays free-form",
			chores:  "custom-chore",
			classes: "custom-chore=a,b",
			want:    map[string][]string{"custom-chore": nil},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Load(Knobs{Chores: tc.chores, Classes: tc.classes})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			for _, c := range got {
				if !reflect.DeepEqual(c.ClassList, tc.want[c.Name]) {
					t.Errorf("%s ClassList = %#v, want %#v", c.Name, c.ClassList, tc.want[c.Name])
				}
			}
		})
	}
}

func TestLoadClassListDoesNotAliasCatalog(t *testing.T) {
	got, err := Load(Knobs{Chores: "refactor", Classes: "refactor=extra"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got[0].ClassList[0] = "mutated"
	if builtinClassLists["refactor"][0] == "mutated" {
		t.Error("Load aliased builtinClassLists")
	}
}

// withoutClassList blanks ClassList so TestLoad's table keeps pinning the
// knob-derived fields; TestLoadClassList pins the catalog-derived one.
func withoutClassList(cs []Chore) []Chore {
	if cs == nil {
		return nil
	}
	out := make([]Chore, len(cs))
	for i, c := range cs {
		c.ClassList = nil
		out[i] = c
	}
	return out
}
