package inputdoc

import (
	"strings"
	"testing"
)

func TestFlagEntryArgv_FlaglessKnobPanics(t *testing.T) {
	defer func() {
		r := recover()
		msg, _ := r.(string)
		if !strings.Contains(msg, "NO_FLAG_KNOB") {
			t.Fatalf("recover = %v, want a panic naming NO_FLAG_KNOB", r)
		}
	}()
	FlagEntry{Env: "NO_FLAG_KNOB"}.Argv("x")
	t.Fatal("Argv on a flagless knob did not panic")
}
