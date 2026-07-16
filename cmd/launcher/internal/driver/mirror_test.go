package driver

import (
	"testing"

	"spindrift.dev/launcher/internal/driver/claude"
)

// TestClassReasonEnumsMirror guards the duplicated Class/Reason vocabulary
// (driver vs driver/claude, kept separate to avoid an import cycle per
// claude/classify.go's package doc) against silent drift: claude.go bridges
// the two via an unchecked string cast in ClassifyTransient, so a value
// added to one side but not the other compiles clean and only misbehaves at
// runtime in retry-dispatch logic.
func TestClassReasonEnumsMirror(t *testing.T) {
	driverClasses := []string{string(Transient), string(Terminal)}
	claudeClasses := []string{string(claude.Transient), string(claude.Terminal)}
	assertSameSet(t, "Class", driverClasses, claudeClasses)

	driverReasons := []string{string(RateLimit), string(Overloaded), string(Network), string(TaskFailed)}
	claudeReasons := []string{string(claude.RateLimit), string(claude.Overloaded), string(claude.Network), string(claude.TaskFailed)}
	assertSameSet(t, "Reason", driverReasons, claudeReasons)
}

func assertSameSet(t *testing.T, label string, a, b []string) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("%s: driver has %d values, claude has %d: %v vs %v", label, len(a), len(b), a, b)
	}
	aSet := make(map[string]bool, len(a))
	for _, v := range a {
		aSet[v] = true
	}
	bSet := make(map[string]bool, len(b))
	for _, v := range b {
		bSet[v] = true
	}
	for _, v := range a {
		if !bSet[v] {
			t.Errorf("%s: driver value %q has no matching claude value", label, v)
		}
	}
	for _, v := range b {
		if !aSet[v] {
			t.Errorf("%s: claude value %q has no matching driver value", label, v)
		}
	}
}
