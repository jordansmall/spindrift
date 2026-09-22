package main

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/freshness"
)

// stubPodmanMachineMemory swaps the query seam for the duration of the test,
// restoring the real implementation via t.Cleanup so later tests never see a
// stale fake. It mutates a process-wide var, so no test using it may call
// t.Parallel().
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

func TestPodmanMachineMemoryCheck_BwrapReturnsNoRow(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	c.runtime = "podman"
	if got := podmanMachineMemoryCheck(c); got != nil {
		t.Errorf("podmanMachineMemoryCheck() = %v, want nil under bwrap", got)
	}
}

func TestPodmanMachineMemoryCheck_NonPodmanRuntimeNotApplicable(t *testing.T) {
	called := stubPodmanMachineMemory(t, 4096, true)

	c := minimalValidConfig()
	c.runtime = "docker"
	checks := podmanMachineMemoryCheck(c)
	if len(checks) != 1 {
		t.Fatalf("podmanMachineMemoryCheck() returned %d rows, want 1", len(checks))
	}
	ch := checks[0]
	if ch.Name != podmanMachineMemoryCheckName {
		t.Errorf("Name = %q, want %q", ch.Name, podmanMachineMemoryCheckName)
	}
	output, err := ch.Probe()
	if err != nil {
		t.Fatalf("Probe() = %v, want nil (not applicable is a success)", err)
	}
	msg := ch.SuccessMsg(output)
	if !strings.Contains(msg, "not applicable") || !strings.Contains(msg, "docker") {
		t.Errorf("SuccessMsg = %q, want it to say not applicable and name the runtime", msg)
	}
	if *called {
		t.Errorf("the machine-query seam was called for a non-podman runtime, want it skipped")
	}
}

func TestPodmanMachineMemoryCheck_EmptyMemoryLimitNotApplicableAndSeamNeverCalled(t *testing.T) {
	called := stubPodmanMachineMemory(t, 4096, true)

	c := minimalValidConfig()
	c.runtime = "podman"
	c.memoryLimit = ""
	c.maxParallel = 3

	ch := checkByName(t, podmanMachineMemoryCheck(c), podmanMachineMemoryCheckName)
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

func TestPodmanMachineMemoryCheck_NoActiveMachineNotApplicable(t *testing.T) {
	stubPodmanMachineMemory(t, 0, false)

	c := minimalValidConfig()
	c.runtime = "podman"
	c.memoryLimit = "5g"
	c.maxParallel = 2

	ch := checkByName(t, podmanMachineMemoryCheck(c), podmanMachineMemoryCheckName)
	output, err := ch.Probe()
	if err != nil {
		t.Fatalf("Probe() = %v, want nil", err)
	}
	msg := ch.SuccessMsg(output)
	if !strings.Contains(msg, "not applicable") || !strings.Contains(msg, "podman machine") {
		t.Errorf("SuccessMsg = %q, want it to say not applicable and mention no active podman machine", msg)
	}
}

func TestPodmanMachineMemoryCheck_BelowThresholdErrorsWithArithmeticAndRemedyNamesAllThreeFixes(t *testing.T) {
	// 5g == 5120MiB; x2 + 512 overhead = 10752MiB required. 8000MiB is short.
	stubPodmanMachineMemory(t, 8000, true)

	c := minimalValidConfig()
	c.runtime = "podman"
	c.memoryLimit = "5g"
	c.maxParallel = 2

	ch := checkByName(t, podmanMachineMemoryCheck(c), podmanMachineMemoryCheckName)
	_, err := ch.Probe()
	if err == nil {
		t.Fatalf("Probe() = nil, want an error for a machine below the required threshold")
	}
	for _, want := range []string{"8000", "5g", "2", "10752"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Probe() error = %q, want it to contain %q", err.Error(), want)
		}
	}
	if !strings.Contains(err.Error(), "OOM-killer") {
		t.Errorf("Probe() error = %q, want it to name the OOM-killer ordering", err.Error())
	}

	remedy := ch.Remedy
	for _, want := range []string{"MAX_PARALLEL", "podman machine set --memory 10752", "MEMORY_LIMIT"} {
		if !strings.Contains(remedy, want) {
			t.Errorf("Remedy = %q, want it to contain %q", remedy, want)
		}
	}
}

func TestPodmanMachineMemoryCheck_AtOrAboveThresholdSucceeds(t *testing.T) {
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

			c := minimalValidConfig()
			c.runtime = "podman"
			c.memoryLimit = "5g"
			c.maxParallel = tc.maxParallel

			ch := checkByName(t, podmanMachineMemoryCheck(c), podmanMachineMemoryCheckName)
			if _, err := ch.Probe(); err != nil {
				t.Errorf("Probe() = %v, want nil at or above the required threshold", err)
			}
		})
	}
}

func TestPodmanMachineMemoryCheck_UnparseableMemoryLimitWrapsErrDegraded(t *testing.T) {
	stubPodmanMachineMemory(t, 4096, true)

	c := minimalValidConfig()
	c.runtime = "podman"
	c.memoryLimit = "not-a-limit"
	c.maxParallel = 1

	ch := checkByName(t, podmanMachineMemoryCheck(c), podmanMachineMemoryCheckName)
	_, err := ch.Probe()
	if !errors.Is(err, doctor.ErrDegraded) {
		t.Errorf("Probe() = %v, want it to wrap doctor.ErrDegraded", err)
	}

	assertUnparseableRemedy(t, ch.Remedy)
}

// The config schema applies no format validation to MEMORY_LIMIT, so a
// plausible-looking "5x" reaches the remedy exactly like "not-a-limit" does
// and must get the same fallback.
func TestPodmanMachineMemoryCheck_UnparseableMemoryLimitNoFormatValidation(t *testing.T) {
	stubPodmanMachineMemory(t, 4096, true)

	c := minimalValidConfig()
	c.runtime = "podman"
	c.memoryLimit = "5x"
	c.maxParallel = 1

	ch := checkByName(t, podmanMachineMemoryCheck(c), podmanMachineMemoryCheckName)
	if _, err := ch.Probe(); !errors.Is(err, doctor.ErrDegraded) {
		t.Errorf("Probe() = %v, want it to wrap doctor.ErrDegraded", err)
	}

	assertUnparseableRemedy(t, ch.Remedy)
}

// assertUnparseableRemedy asserts the fallback an unparseable memoryLimit
// must produce. It asserts the absence of "podman machine set --memory"
// rather than the presence of some placeholder: that phrase is a strict
// prefix of the parsed branch's own command, so only the negative form
// actually discriminates the two branches.
func assertUnparseableRemedy(t *testing.T, remedy string) {
	t.Helper()
	if strings.Contains(remedy, "podman machine set --memory") {
		t.Errorf("Remedy = %q, want no podman machine set --memory command when the limit is unparseable (no figure to size it with)", remedy)
	}
	for _, want := range []string{"MAX_PARALLEL", "MEMORY_LIMIT", "RAM"} {
		if !strings.Contains(remedy, want) {
			t.Errorf("Remedy = %q, want it to contain %q", remedy, want)
		}
	}
}

// 2g == 2048MiB, x3 + 512MiB overhead = 6656MiB.
func TestPodmanMachineMemoryRemedy_ParsedLimitEmitsCopyPasteableCommand(t *testing.T) {
	remedy := podmanMachineMemoryRemedy("2g", 3)
	if !strings.Contains(remedy, "podman machine set --memory 6656") {
		t.Errorf("podmanMachineMemoryRemedy(%q, %d) = %q, want it to contain the literal command %q", "2g", 3, remedy, "podman machine set --memory 6656")
	}
}

// The Probe's failure message and the Remedy both name a required-MiB
// figure derived from the same inputs; they must never drift apart.
func TestPodmanMachineMemoryCheck_ProbeAndRemedyFiguresNeverDesync(t *testing.T) {
	stubPodmanMachineMemory(t, 8000, true)

	c := minimalValidConfig()
	c.runtime = "podman"
	c.memoryLimit = "5g"
	c.maxParallel = 2

	ch := checkByName(t, podmanMachineMemoryCheck(c), podmanMachineMemoryCheckName)
	_, err := ch.Probe()
	if err == nil {
		t.Fatalf("Probe() = nil, want an error for a machine below the required threshold")
	}

	const wantRequired = "10752"
	if !strings.Contains(err.Error(), wantRequired) {
		t.Fatalf("Probe() error = %q, want it to contain the required figure %q", err.Error(), wantRequired)
	}
	if !strings.Contains(ch.Remedy, "podman machine set --memory "+wantRequired) {
		t.Errorf("Remedy = %q, want it to name the same required figure %q as Probe()'s error %q", ch.Remedy, wantRequired, err.Error())
	}
}

func TestPodmanMachineMemoryCheck_TierIsRequired(t *testing.T) {
	c := minimalValidConfig()
	c.runtime = "podman"
	ch := checkByName(t, podmanMachineMemoryCheck(c), podmanMachineMemoryCheckName)
	if ch.Tier != doctor.Required {
		t.Errorf("Tier = %v, want doctor.Required", ch.Tier)
	}
}

// SuccessMsg must not depend on a variable a sibling closure (Probe) set on
// the side, so a call with no Probe run at all must still name the row
// rather than render an empty message.
func TestPodmanMachineMemoryCheck_SuccessMsgWithoutProbeNamesRow(t *testing.T) {
	c := minimalValidConfig()
	c.runtime = "podman"
	ch := checkByName(t, podmanMachineMemoryCheck(c), podmanMachineMemoryCheckName)

	msg := ch.SuccessMsg(nil)
	if msg == "" {
		t.Fatalf("SuccessMsg(nil) = %q, want a non-empty message naming the row", msg)
	}
	if !strings.Contains(msg, podmanMachineMemoryCheckName) {
		t.Errorf("SuccessMsg(nil) = %q, want it to name the row %q", msg, podmanMachineMemoryCheckName)
	}
}

// Two distinct outputs must render two distinct messages, which only holds
// if SuccessMsg formats from its own parameter.
func TestPodmanMachineMemoryCheck_SuccessMsgFormatsFromArgument(t *testing.T) {
	stubPodmanMachineMemory(t, 999999, true)

	c := minimalValidConfig()
	c.runtime = "podman"
	c.memoryLimit = "5g"
	c.maxParallel = 1

	ch := checkByName(t, podmanMachineMemoryCheck(c), podmanMachineMemoryCheckName)
	output, err := ch.Probe()
	if err != nil {
		t.Fatalf("Probe() = %v, want nil", err)
	}

	got := ch.SuccessMsg(output)
	other := ch.SuccessMsg("a distinguishable other value")
	if got == other {
		t.Errorf("SuccessMsg(output) = %q, SuccessMsg(other) = %q, want distinct messages for distinct outputs", got, other)
	}
	if !strings.Contains(got, podmanMachineMemoryCheckName) {
		t.Errorf("SuccessMsg(output) = %q, want it to name the row", got)
	}
}

// Issue #3544: unlike the bwrap/drift/transport rows, podman-machine-memory
// must appear in classify too -- it is the one extraCheck row whose Required
// tier is meant to fail the run, since Run's own extraChecks are
// informational-only by contract (doctor.go) and classify is the only seam
// that can make a row fatal.
func TestDoctorCheckSets_PutsPodmanMachineMemoryInBothClassifyAndReport(t *testing.T) {
	c := minimalValidConfig()
	c.runtime = "podman"

	classify, report := doctorCheckSets(c)

	checkByName(t, classify, podmanMachineMemoryCheckName)
	// This config is neither bwrap nor route-declaring, and leaves
	// signalCarrier at its log default, so doctorCheckSets' true append order
	// (extra, bwrap, podman-machine-memory, perRoute, drift,
	// registry-proxy-transport, signal-socket-transport) collapses to the
	// extra rows, then podman-machine-memory, then registry-proxy-transport
	// -- pin that position rather than only presence.
	gotIdx := -1
	for i, ch := range report {
		if ch.Name == podmanMachineMemoryCheckName {
			gotIdx = i
			break
		}
	}
	if wantIdx := len(doctorExtraChecks(c)); gotIdx != wantIdx {
		t.Errorf("podman-machine-memory row is at index %d, want %d (immediately after the extra rows)", gotIdx, wantIdx)
	}
	if last := report[len(report)-1].Name; last != registryProxyTransportCheckName {
		t.Errorf("report's last row is %q, want %q", last, registryProxyTransportCheckName)
	}
}

// Issue #3544: the podman-machine-memory row's Required tier must actually
// fail the run, so it belongs in doctorCheckSets' classify half too, not
// only report. An undersized machine makes classify contain a failing row
// and validateConfigChecks name it and its remedy.
func TestDoctorCheckSets_UndersizedPodmanMachineFailsClassify(t *testing.T) {
	stubPodmanMachineMemory(t, 8000, true) // needs 10752MiB (5g x 2 + 512 overhead)

	c := minimalValidConfig()
	c.runtime = "podman"
	c.memoryLimit = "5g"
	c.maxParallel = 2

	classify, _ := doctorCheckSets(c)
	checkByName(t, classify, podmanMachineMemoryCheckName)

	err := validateConfigChecks(c, classify)
	if err == nil {
		t.Fatalf("validateConfigChecks() = nil, want an error for an undersized podman machine")
	}
	if !strings.Contains(err.Error(), "podman machine has 8000MiB RAM") {
		t.Errorf("validateConfigChecks() error = %q, want it to carry the row's probe failure", err.Error())
	}
	if !strings.Contains(err.Error(), "podman machine set --memory 10752") {
		t.Errorf("validateConfigChecks() error = %q, want it to carry the row's remedy", err.Error())
	}
}

// End-to-end through the exit-code vocabulary: an undersized machine must
// make cmdDoctor exit 2 "configuration invalid", the same path
// doctorReport/readContext.validation() drive. This test composes
// validateConfigChecks + doctorExitCodeFor directly rather than going
// through readContext/doctorReport, since building a fake IssueTracker and
// CodeForge just to reach this composition would exercise nothing extra:
// doctorReport's exit code is exactly doctorExitCodeFor(v.configErr, runErr),
// and v.configErr is exactly this validateConfigChecks call.
func TestDoctorCheckSets_UndersizedPodmanMachineExitsTwo(t *testing.T) {
	stubPodmanMachineMemory(t, 8000, true)

	c := minimalValidConfig()
	c.runtime = "podman"
	c.memoryLimit = "5g"
	c.maxParallel = 2

	classify, _ := doctorCheckSets(c)
	configErr := validateConfigChecks(c, classify)
	if got := doctorExitCodeFor(configErr, nil); got != 2 {
		t.Errorf("doctorExitCodeFor(configErr, nil) = %d, want 2 for an undersized podman machine", got)
	}
}

// An adequately-sized machine must never fail the run: classify's row passes
// and the composed exit code stays 0.
func TestDoctorCheckSets_AdequatePodmanMachinePassesClassifyAndExitsZero(t *testing.T) {
	stubPodmanMachineMemory(t, 999999, true)

	c := minimalValidConfig()
	c.runtime = "podman"
	c.memoryLimit = "5g"
	c.maxParallel = 1

	classify, _ := doctorCheckSets(c)
	if err := validateConfigChecks(c, classify); err != nil {
		t.Errorf("validateConfigChecks() = %v, want nil for an adequately-sized podman machine", err)
	}
	if got := doctorExitCodeFor(validateConfigChecks(c, classify), nil); got != 0 {
		t.Errorf("doctorExitCodeFor(...) = %d, want 0 for an adequately-sized podman machine", got)
	}
}

// Each "not applicable" branch must keep passing now that the row also sits
// in classify, or a host with no podman machine at all (the common case)
// would start failing spindrift doctor.
func TestDoctorCheckSets_PodmanMachineNotApplicableBranchesNeverFailClassify(t *testing.T) {
	tests := []struct {
		name string
		mod  func(c *config)
	}{
		{name: "non-podman runtime", mod: func(c *config) { c.runtime = "docker" }},
		{name: "empty MEMORY_LIMIT", mod: func(c *config) { c.runtime = "podman"; c.memoryLimit = "" }},
		{name: "no active machine", mod: func(c *config) {
			c.runtime = "podman"
			c.memoryLimit = "5g"
			stubPodmanMachineMemory(t, 0, false)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := minimalValidConfig()
			tc.mod(&c)

			classify, _ := doctorCheckSets(c)
			if err := validateConfigChecks(c, classify); err != nil {
				t.Errorf("validateConfigChecks() = %v, want nil for the %q branch", err, tc.name)
			}
		})
	}
}

// classify and report must share the same memoized probe (issue #3144's
// memoizeCheckProbes), so running both halves' Probes calls the underlying
// machine query exactly once per doctorCheckSets(c) call, not twice.
func TestDoctorCheckSets_PodmanMachineProbeMemoizedAcrossClassifyAndReport(t *testing.T) {
	calls := 0
	orig := podmanMachineMemoryFn
	t.Cleanup(func() { podmanMachineMemoryFn = orig })
	podmanMachineMemoryFn = func() (int, bool) {
		calls++
		return 8000, true
	}

	c := minimalValidConfig()
	c.runtime = "podman"
	c.memoryLimit = "5g"
	c.maxParallel = 2

	classify, report := doctorCheckSets(c)
	checkByName(t, classify, podmanMachineMemoryCheckName).Probe()
	checkByName(t, report, podmanMachineMemoryCheckName).Probe()
	checkByName(t, classify, podmanMachineMemoryCheckName).Probe()

	if calls != 1 {
		t.Errorf("machine-query seam called %d times across classify and report, want exactly 1 (memoized)", calls)
	}
}

// A bwrap runner runs no podman machine at all, so the row must still be
// absent from both halves now that it also sits in classify.
func TestDoctorCheckSets_BwrapRunnerGetsNoPodmanMachineRowInEitherSet(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	c.runtime = "podman"

	classify, report := doctorCheckSets(c)
	for _, ch := range classify {
		if ch.Name == podmanMachineMemoryCheckName {
			t.Errorf("classify contains podman-machine-memory under bwrap, want it absent")
		}
	}
	for _, ch := range report {
		if ch.Name == podmanMachineMemoryCheckName {
			t.Errorf("report contains podman-machine-memory under bwrap, want it absent")
		}
	}
}

// Issue #3544: MEMORY_LIMIT takes no format validation from the schema, so
// garbage like "5x" reaches the probe on a podman host with a live machine
// and degrades the row. doctor prints that as an advisory line, so classify
// must not simultaneously turn it into exit 2 "configuration invalid".
func TestDoctorCheckSets_DegradedPodmanMachineRowNeverFailsClassify(t *testing.T) {
	stubPodmanMachineMemory(t, 4096, true)

	c := minimalValidConfig()
	c.runtime = "podman"
	c.memoryLimit = "5x"

	classify, _ := doctorCheckSets(c)
	checkByName(t, classify, podmanMachineMemoryCheckName)

	configErr := validateConfigChecks(c, classify)
	if configErr != nil {
		t.Fatalf("validateConfigChecks() = %v, want nil for a degraded podman-machine-memory row", configErr)
	}
	if got := doctorExitCodeFor(configErr, nil); got != 0 {
		t.Errorf("doctorExitCodeFor(configErr, nil) = %d, want 0 for a degraded row", got)
	}
}
