package pi_test

import (
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/pi"
)

// atLevel rewrites the thinking level of a rendered assistant line.
func atLevel(line, level string) string {
	return strings.Replace(line, `"thinkingLevel":"high"`, `"thinkingLevel":"`+level+`"`, 1)
}

// The observed id is the bare `<provider>/<model>` a --model flag names, with
// the thinking level on its own event field.
func TestDecode_ObservedModelIsBareConfiguredId(t *testing.T) {
	// The shape --model and ROLE_MODELS carry; the fixture sets provider and
	// model separately.
	const configured = "stub/stub-model"
	for _, ev := range pi.NewDecoder()(textEnd(1000, "hi")) {
		if ev.Kind != driverkit.KindModel && ev.Kind != driverkit.KindUsage {
			continue
		}
		if ev.Model != configured {
			t.Errorf("%s Model = %q, want %q", ev.Kind, ev.Model, configured)
		}
		if ev.Effort != "high" {
			t.Errorf("%s Effort = %q, want high", ev.Kind, ev.Effort)
		}
	}
}

// A thinking-level change mid-Pass on one model is one SummedByModel row.
func TestUsage_MidPassLevelChangeKeepsOneModelRow(t *testing.T) {
	path := pi.WriteLog(t, textEnd(1000, "a"), atLevel(textEnd(2000, "b"), "low"))
	rep, err := newDriver().ExtractUsage(path)
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	if len(rep.SummedByModel) != 1 || rep.SummedByModel[0].Model != "stub/stub-model" {
		t.Errorf("SummedByModel = %+v, want one stub/stub-model row", rep.SummedByModel)
	}
}

// A subagent on the same model at another effort adds to that model's row.
func TestUsage_SameModelSubagentAtOtherEffortKeepsOneModelRow(t *testing.T) {
	child := strings.Replace(atLevel(childAssistant(1500, `[]`), "low"), "child-model", "stub-model", 1)
	path := pi.WriteLog(t,
		toolTurn(1000, "s1", "subagent", `{"agent":"scout","task":"look"}`),
		subagentEnd("single", subagentResult("scout", child)),
		textEnd(2000, "done"),
	)
	rep, err := newDriver().ExtractUsage(path)
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	if len(rep.SummedByModel) != 1 || rep.SummedByModel[0].Model != "stub/stub-model" {
		t.Errorf("SummedByModel = %+v, want one stub/stub-model row", rep.SummedByModel)
	}
}

// A mid-Pass level change on one model is one model and both efforts.
func TestReadPasses_MidPassLevelChangeKeepsOneModelBothEfforts(t *testing.T) {
	path := pi.WriteLog(t, textEnd(1000, "a"), atLevel(textEnd(2000, "b"), "low"))
	passes, err := newDriver().ReadPasses(path, "", nil)
	if err != nil || len(passes) != 1 {
		t.Fatalf("ReadPasses: %v, %d passes", err, len(passes))
	}
	if want := []string{"stub/stub-model"}; !reflect.DeepEqual(passes[0].Models, want) {
		t.Errorf("Models = %v, want %v", passes[0].Models, want)
	}
	if want := []string{"high", "low"}; !reflect.DeepEqual(passes[0].Efforts, want) {
		t.Errorf("Efforts = %v, want %v", passes[0].Efforts, want)
	}
}

// A same-model subagent at another effort adds an effort, not a model.
func TestReadPasses_SameModelSubagentAtOtherEffortAddsEffort(t *testing.T) {
	child := strings.Replace(atLevel(childAssistant(1500, `[]`), "low"), "child-model", "stub-model", 1)
	path := pi.WriteLog(t,
		toolTurn(1000, "s1", "subagent", `{"agent":"scout","task":"look"}`),
		subagentEnd("single", subagentResult("scout", child)),
		textEnd(2000, "done"),
	)
	passes, err := newDriver().ReadPasses(path, "", nil)
	if err != nil || len(passes) != 1 {
		t.Fatalf("ReadPasses: %v, %d passes", err, len(passes))
	}
	if want := []string{"stub/stub-model"}; !reflect.DeepEqual(passes[0].Models, want) {
		t.Errorf("Models = %v, want %v", passes[0].Models, want)
	}
	if want := []string{"high", "low"}; !reflect.DeepEqual(passes[0].Efforts, want) {
		t.Errorf("Efforts = %v, want %v", passes[0].Efforts, want)
	}
}
