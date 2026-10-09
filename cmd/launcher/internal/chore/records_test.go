package chore_test

import (
	"reflect"
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatchrecord"
)

func TestNewSettledRecords(t *testing.T) {
	settled := dispatchrecord.OutcomeSourceSettled
	seq := int64(0)
	rec := func(id, source string) dispatchrecord.Record {
		seq++
		return dispatchrecord.Record{ID: id, OutcomeSource: source, SettledSeq: seq}
	}
	all := []dispatchrecord.Record{
		rec("a", settled), rec("b", "log"), rec("c", settled), rec("d", settled),
	}

	tests := []struct {
		name    string
		records []dispatchrecord.Record
		cursor  string
		want    []string
	}{
		{"empty cursor yields every settled record", all, "", []string{"a", "c", "d"}},
		{"records strictly after the cursor", all, "a", []string{"c", "d"}},
		{"cursor on an unsettled record yields every settled record", all, "b", []string{"a", "c", "d"}},
		{"cursor at the newest record yields none", all, "d", nil},
		{"unknown cursor yields every settled record", all, "gone", []string{"a", "c", "d"}},
		{"no records", nil, "a", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ids []string
			for _, r := range chore.NewSettledRecords(tt.records, tt.cursor) {
				ids = append(ids, r.ID)
			}
			if !reflect.DeepEqual(ids, tt.want) {
				t.Errorf("NewSettledRecords(%q) ids = %v, want %v", tt.cursor, ids, tt.want)
			}
		})
	}
}

// A Record joins the window when it settles, not when it was claimed: A is
// claimed first but settles after B, so a cursor on B must still see it.
func TestNewSettledRecordsOrdersBySettleNotClaim(t *testing.T) {
	rec := func(id string, seq int64) dispatchrecord.Record {
		return dispatchrecord.Record{ID: id, OutcomeSource: dispatchrecord.OutcomeSourceSettled, SettledSeq: seq}
	}
	records := []dispatchrecord.Record{rec("a", 3), rec("b", 1), rec("c", 2)}
	orig := slices.Clone(records)

	ids := func(cursor string) []string {
		var out []string
		for _, r := range chore.NewSettledRecords(records, cursor) {
			out = append(out, r.ID)
		}
		return out
	}
	if got, want := ids(""), []string{"b", "c", "a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("empty cursor = %v, want %v", got, want)
	}
	if got, want := ids("b"), []string{"c", "a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("cursor b = %v, want %v", got, want)
	}
	if got := ids("a"); got != nil {
		t.Errorf("cursor a = %v, want none", got)
	}
	if !reflect.DeepEqual(records, orig) {
		t.Errorf("caller's slice was reordered: %v", records)
	}
}
