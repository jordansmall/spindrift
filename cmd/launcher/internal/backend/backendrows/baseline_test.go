package backendrows_test

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBaselineMatchesGolden(t *testing.T) {
	t.Parallel()
	got, err := backendrows.Baseline(readTestdata(t, "baseline-rows.json"))
	if err != nil {
		t.Fatalf("Baseline: %v", err)
	}
	if want := readTestdata(t, "baseline.golden.json"); !bytes.Equal(got, want) {
		t.Errorf("baseline differs from testdata/baseline.golden.json\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

func TestBaselineRefusesAnEmptyDocument(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "[]", "null"} {
		if got, err := backendrows.Baseline(json.RawMessage(raw)); err == nil {
			t.Errorf("Baseline(%q) = %s, want an error", raw, got)
		}
	}
}

// What Baseline writes must read back as an accepted baseline that diffs clean
// against the rows it came from, in the shape lib's Nix reader decodes.
func TestBaselineRoundTripsToNoChangeAndNoWidening(t *testing.T) {
	t.Parallel()
	raw := readTestdata(t, "baseline-rows.json")
	out, err := backendrows.Baseline(raw)
	if err != nil {
		t.Fatalf("Baseline: %v", err)
	}
	var rows []backendrows.Row
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	var entries map[string]map[string]json.RawMessage
	if err := json.Unmarshal(out, &entries); err != nil {
		t.Fatalf("written file is not JSON: %v", err)
	}
	for _, row := range rows {
		entry, ok := entries[row.Name]
		if !ok {
			t.Fatalf("no entry for %q", row.Name)
		}
		var routes []connectorhost.Route
		if err := json.Unmarshal(entry["routes"], &routes); err != nil {
			t.Fatalf("%s routes: %v", row.Name, err)
		}
		delete(entry, "routes")
		grantJSON, _ := json.Marshal(entry)
		var grant backendrows.Grant
		dec := json.NewDecoder(bytes.NewReader(grantJSON))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&grant); err != nil {
			t.Fatalf("%s grant: %v", row.Name, err)
		}
		row.AcceptedRoutes, row.AcceptedGrant = routes, &grant
		if c := row.GrantChanges(); len(c) != 0 {
			t.Errorf("%s: grant changes %v, want none", row.Name, c)
		}
		if w := row.Widening(); len(w) != 0 {
			t.Errorf("%s: widening %v, want none", row.Name, w)
		}
	}
}
