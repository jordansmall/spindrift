package console

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/console/msgcensus"
)

// TestMsgCensusMatchesDeclaredMsgTypes guards against a Msg type being added
// or removed without regenerating msg_census_gen.go: it independently
// AST-walks this package's msg_*.go declarations via msgcensus.Collect and
// compares the result against the generated msgCensus census.
func TestMsgCensusMatchesDeclaredMsgTypes(t *testing.T) {
	declared, err := msgcensus.Collect(".")
	if err != nil {
		t.Fatalf("msgcensus.Collect(\".\"): %v", err)
	}

	declaredSet := make(map[string]struct{}, len(declared))
	for _, name := range declared {
		declaredSet[name] = struct{}{}
	}

	censusSet := make(map[string]struct{}, len(msgCensus))
	for _, name := range msgCensus {
		censusSet[name] = struct{}{}
	}

	var missing, extra []string
	for _, name := range declared {
		if _, ok := censusSet[name]; !ok {
			missing = append(missing, name)
		}
	}
	for _, name := range msgCensus {
		if _, ok := declaredSet[name]; !ok {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	if len(missing) == 0 && len(extra) == 0 {
		if !reflect.DeepEqual(declared, msgCensus) {
			t.Errorf("msgCensus lists the same Msg types as declared but out of sorted order: want %v, got %v. Regenerate with `go generate ./cmd/launcher/internal/console`.", declared, msgCensus)
		}
		return
	}

	var b strings.Builder
	b.WriteString("msgCensus is out of date with the declared Msg types.\n")
	if len(missing) > 0 {
		b.WriteString("missing from msgCensus (declared but not listed):\n")
		for _, name := range missing {
			b.WriteString("  - " + name + "\n")
		}
	}
	if len(extra) > 0 {
		b.WriteString("extra in msgCensus (listed but not declared):\n")
		for _, name := range extra {
			b.WriteString("  - " + name + "\n")
		}
	}
	b.WriteString("Regenerate with `go generate ./cmd/launcher/internal/console`.\n")

	t.Error(b.String())
}
