package oci

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
)

// stubPodmanMachineMemory swaps the query seam for the duration of the test.
// It mutates a process-wide var, so no test using it may call t.Parallel().
func stubPodmanMachineMemory(t *testing.T, mib int, found bool) *bool {
	t.Helper()
	called := false
	orig := podmanMachineMemoryFn
	t.Cleanup(func() { podmanMachineMemoryFn = orig })
	podmanMachineMemoryFn = func() (int, bool) {
		called = true
		return mib, found
	}
	return &called
}

func podmanCfg(memoryLimit string, maxParallel int) runner.Config {
	return runner.Config{Runtime: "podman", MemoryLimit: memoryLimit, MaxParallel: maxParallel}
}

func machinePrereq(t *testing.T, cfg runner.Config) runner.Prerequisite {
	t.Helper()
	for _, p := range Prerequisites(cfg, runnertest.Lookup(nil)) {
		if p.Name == podmanMachineMemoryCheckName {
			return p
		}
	}
	t.Fatalf("Prerequisites(%+v) has no %q row", cfg, podmanMachineMemoryCheckName)
	return runner.Prerequisite{}
}

func TestStrategyDeclaresPrerequisites(t *testing.T) {
	if Strategy.Prerequisites == nil {
		t.Fatal("Strategy.Prerequisites is nil")
	}
}

func TestPrerequisites_DoctorOnlyRequiredRow(t *testing.T) {
	p := machinePrereq(t, podmanCfg("5g", 1))
	if p.Launch != runner.DoctorOnly {
		t.Errorf("Launch = %v, want DoctorOnly", p.Launch)
	}
	if !p.Required {
		t.Error("Required = false, want true")
	}
	if ch := doctor.PrerequisiteCheck(p); ch.Tier != doctor.Required {
		t.Errorf("Tier = %v, want doctor.Required", ch.Tier)
	}
}

func TestPrerequisites_NonPodmanRuntimeNotApplicable(t *testing.T) {
	for _, rt := range []string{"docker", "rancher"} {
		t.Run(rt, func(t *testing.T) {
			called := stubPodmanMachineMemory(t, 4096, true)
			cfg := podmanCfg("5g", 2)
			cfg.Runtime = rt
			ch := doctor.PrerequisiteCheck(machinePrereq(t, cfg))
			output, err := ch.Probe()
			if err != nil {
				t.Fatalf("Probe() = %v, want nil (not applicable is a success)", err)
			}
			want := `podman-machine-memory (not applicable (runtime "` + rt + `" uses no podman machine))`
			if msg := ch.SuccessMsg(output); msg != want {
				t.Errorf("SuccessMsg = %q, want %q", msg, want)
			}
			if *called {
				t.Errorf("the machine-query seam was called for a non-podman runtime, want it skipped")
			}
		})
	}
}

func TestPrerequisites_EmptyMemoryLimitNotApplicableAndSeamNeverCalled(t *testing.T) {
	called := stubPodmanMachineMemory(t, 4096, true)

	ch := doctor.PrerequisiteCheck(machinePrereq(t, podmanCfg("", 3)))
	output, err := ch.Probe()
	if err != nil {
		t.Fatalf("Probe() = %v, want nil", err)
	}
	msg := ch.SuccessMsg(output)
	if !strings.Contains(msg, "not applicable") || !strings.Contains(msg, "MEMORY_LIMIT") {
		t.Errorf("SuccessMsg = %q, want it to say not applicable and mention MEMORY_LIMIT", msg)
	}
	if *called {
		t.Errorf("the machine-query seam was called despite MEMORY_LIMIT being empty (deliberate opt-out), want it skipped")
	}
}

func TestPrerequisites_NoActiveMachineNotApplicable(t *testing.T) {
	stubPodmanMachineMemory(t, 0, false)

	ch := doctor.PrerequisiteCheck(machinePrereq(t, podmanCfg("5g", 2)))
	output, err := ch.Probe()
	if err != nil {
		t.Fatalf("Probe() = %v, want nil", err)
	}
	msg := ch.SuccessMsg(output)
	if !strings.Contains(msg, "not applicable") || !strings.Contains(msg, "podman machine") {
		t.Errorf("SuccessMsg = %q, want it to say not applicable and mention no active podman machine", msg)
	}
}

func TestPrerequisites_BelowThresholdErrorsWithArithmeticAndRemedyNamesAllThreeFixes(t *testing.T) {
	// 5g == 5120MiB; x2 + 512 overhead = 10752MiB required. 8000MiB is short.
	stubPodmanMachineMemory(t, 8000, true)

	p := machinePrereq(t, podmanCfg("5g", 2))
	_, err := p.Probe()
	if err == nil {
		t.Fatalf("Probe() = nil, want an error for a machine below the required threshold")
	}
	for _, want := range []string{"8000", "5g", "2", "10752", "OOM-killer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Probe() error = %q, want it to contain %q", err.Error(), want)
		}
	}
	for _, want := range []string{"MAX_PARALLEL", "podman machine set --memory 10752", "MEMORY_LIMIT"} {
		if !strings.Contains(p.Remedy, want) {
			t.Errorf("Remedy = %q, want it to contain %q", p.Remedy, want)
		}
	}
}

func TestPrerequisites_AtOrAboveThresholdSucceeds(t *testing.T) {
	tests := []struct {
		name        string
		machineMiB  int
		maxParallel int
	}{
		// Required = 5120*1 + 512 = 5632MiB exactly.
		{name: "exactly at threshold", machineMiB: 5632, maxParallel: 1},
		{name: "above threshold", machineMiB: 999999, maxParallel: 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubPodmanMachineMemory(t, tc.machineMiB, true)
			p := machinePrereq(t, podmanCfg("5g", tc.maxParallel))
			if _, err := p.Probe(); err != nil {
				t.Errorf("Probe() = %v, want nil at or above the required threshold", err)
			}
		})
	}
}

func TestPrerequisites_InvalidMemoryLimitWrapsErrDegraded(t *testing.T) {
	// "-1g" and "0" parse as integers but are not usable limits; the config
	// schema applies no format validation, so "5x" reaches here too.
	for _, limit := range []string{"not-a-limit", "-1g", "0", "5x"} {
		t.Run(limit, func(t *testing.T) {
			stubPodmanMachineMemory(t, 4096, true)
			p := machinePrereq(t, podmanCfg(limit, 1))
			if _, err := p.Probe(); !errors.Is(err, doctor.ErrDegraded) {
				t.Errorf("Probe() = %v, want it to wrap doctor.ErrDegraded", err)
			}
			assertInvalidRemedy(t, p.Remedy)
		})
	}
}

// A degraded Required row must never block, so classification stays clean.
func TestPrerequisites_DegradedRowIsNotBlocking(t *testing.T) {
	stubPodmanMachineMemory(t, 4096, true)
	ch := doctor.PrerequisiteCheck(machinePrereq(t, podmanCfg("5x", 1)))
	if r := doctor.RunChecks([]doctor.Check{ch})[0]; doctor.Blocking(r) {
		t.Errorf("degraded row is Blocking: %v", r.Err)
	}
}

// assertInvalidRemedy asserts the fallback an invalid memoryLimit must
// produce: the fix is MEMORY_LIMIT itself, so "lower MEMORY_LIMIT" (wrong
// advice for "0") must not appear. It asserts the absence of "podman machine
// set --memory" because that phrase is a strict prefix of the parsed branch's
// own command, so only the negative form discriminates the two branches.
func assertInvalidRemedy(t *testing.T, remedy string) {
	t.Helper()
	if strings.Contains(remedy, "podman machine set --memory") {
		t.Errorf("Remedy = %q, want no podman machine set --memory command when the limit is invalid", remedy)
	}
	if !strings.Contains(remedy, "MEMORY_LIMIT") {
		t.Errorf("Remedy = %q, want it to contain %q", remedy, "MEMORY_LIMIT")
	}
	if strings.Contains(remedy, "lower MEMORY_LIMIT") {
		t.Errorf("Remedy = %q, want no %q advice: the value itself is invalid", remedy, "lower MEMORY_LIMIT")
	}
}

// 2g == 2048MiB, x3 + 512MiB overhead = 6656MiB.
func TestPodmanMachineMemoryRemedy_ParsedLimitEmitsCopyPasteableCommand(t *testing.T) {
	remedy := podmanMachineMemoryRemedy("2g", 3)
	if !strings.Contains(remedy, "podman machine set --memory 6656") {
		t.Errorf("podmanMachineMemoryRemedy(%q, %d) = %q, want the literal command", "2g", 3, remedy)
	}
}

// The Probe's failure message and the Remedy both name a required-MiB figure
// derived from the same inputs; they must never drift apart.
func TestPrerequisites_ProbeAndRemedyFiguresNeverDesync(t *testing.T) {
	stubPodmanMachineMemory(t, 8000, true)

	p := machinePrereq(t, podmanCfg("5g", 2))
	_, err := p.Probe()
	if err == nil {
		t.Fatalf("Probe() = nil, want an error for a machine below the required threshold")
	}
	const wantRequired = "10752"
	if !strings.Contains(err.Error(), wantRequired) {
		t.Fatalf("Probe() error = %q, want it to contain the required figure %q", err.Error(), wantRequired)
	}
	if !strings.Contains(p.Remedy, "podman machine set --memory "+wantRequired) {
		t.Errorf("Remedy = %q, want it to name the same required figure %q as Probe()'s error %q", p.Remedy, wantRequired, err.Error())
	}
}

// SuccessMsg must not depend on a variable a sibling closure (Probe) set on
// the side, so a call with no Probe run at all must still name the row.
func TestPrerequisites_SuccessMsgWithoutProbeNamesRow(t *testing.T) {
	p := machinePrereq(t, podmanCfg("5g", 1))
	if msg := p.SuccessMsg(nil); msg != podmanMachineMemoryCheckName {
		t.Errorf("SuccessMsg(nil) = %q, want %q", msg, podmanMachineMemoryCheckName)
	}
}

// Two distinct outputs must render two distinct messages, which only holds if
// SuccessMsg formats from its own parameter.
func TestPrerequisites_SuccessMsgFormatsFromArgument(t *testing.T) {
	stubPodmanMachineMemory(t, 999999, true)

	p := machinePrereq(t, podmanCfg("5g", 1))
	output, err := p.Probe()
	if err != nil {
		t.Fatalf("Probe() = %v, want nil", err)
	}
	got := p.SuccessMsg(output)
	if other := p.SuccessMsg("a distinguishable other value"); got == other {
		t.Errorf("SuccessMsg(output) = %q, SuccessMsg(other) = %q, want distinct messages", got, other)
	}
	if want := "podman-machine-memory (podman machine has 999999MiB RAM, 5632MiB required)"; got != want {
		t.Errorf("SuccessMsg(output) = %q, want %q", got, want)
	}
}

func TestPrerequisites_DeclaresRuntimeFirst(t *testing.T) {
	got := Prerequisites(podmanCfg("5g", 1), runnertest.Lookup(nil))
	if len(got) != 2 {
		t.Fatalf("returned %d rows, want 2", len(got))
	}
	if got[0].Name != runner.RuntimePrerequisiteName || got[0].Launch != runner.LaunchWithConfig || !got[0].Required {
		t.Errorf("[0] = %+v, want the Required LaunchWithConfig runtime row", got[0])
	}
	if got[1].Name != podmanMachineMemoryCheckName {
		t.Errorf("[1].Name = %q, want %q", got[1].Name, podmanMachineMemoryCheckName)
	}
}
