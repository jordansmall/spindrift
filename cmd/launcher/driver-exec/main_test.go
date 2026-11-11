package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/drivertest"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/promptassembly"
)

// writeHandoffFile builds the -handoff-file fixture that replaced the
// individually-removed per-pass flags (issue #2975 slice 3).
func writeHandoffFile(t *testing.T, h promptassembly.Handoff) string {
	t.Helper()
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal handoff: %v", err)
	}
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// claudeArgvShape mirrors the argv shape lib/drivers/claude.nix declares.
// It holds what assemble-prompt's CLI wrapper would put in Handoff.ArgvShape
// for a claude Driver.
var claudeArgvShape = promptassembly.ArgvShape{
	PromptStyle: "flag",
	PromptFlag:  "-p",
	ModelFlag:   "--model",
	AgentsFlag:  "--agents",
	EffortFlag:  "--effort",
	Order:       []string{"prompt", "model", "agents", "session", "driverFlags", "effort"},
}

// opencodeArgvShape mirrors the argv shape lib/drivers/opencode.nix declares.
var opencodeArgvShape = promptassembly.ArgvShape{
	PromptStyle:    "positional",
	ModelFlag:      "-m",
	ModelOmitEmpty: true,
	EffortFlag:     "--variant",
	Order:          []string{"driverFlags", "model", "effort", "session", "prompt"},
}

// TestMainRunHandoffFileReproducesClaudeShape pins issue #2975 slice 3: the
// handoff's ArgvShape, not driver-exec, decides the driver's argv shape.
func TestMainRunHandoffFileReproducesClaudeShape(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	driverBin := writeFakeDriver(t, dir, "fake-driver", "echo \"$@\" >> "+callLog+"\nexit 0\n")

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("implement the thing"), 0o600); err != nil {
		t.Fatal(err)
	}
	agentsFile := filepath.Join(dir, "agents.json")
	if err := os.WriteFile(agentsFile, []byte(`{"scout":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	handoffPath := writeHandoffFile(t, promptassembly.Handoff{
		PromptFile: promptFile,
		AgentsFile: agentsFile,
		DriverBin:  driverBin,
		ArgvShape:  claudeArgvShape,
	})

	argv := []string{
		"-handoff-file", handoffPath,
		"-log-path", filepath.Join(dir, "stream.log"),
	}

	var stdout, stderr bytes.Buffer
	rc := mainRun(argv, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	got := strings.TrimSpace(string(calls))
	fields := strings.Fields(got)
	if len(fields) == 0 || fields[0] != "-p" {
		t.Errorf("driver argv = %q, want it to start with \"-p\" (claude's promptFlag)", got)
	}
	if !strings.Contains(got, "--agents") {
		t.Errorf("driver argv = %q, want it to contain \"--agents\" (claude's agentsFlag)", got)
	}
}

// TestMainRunRoleAwareModelEffort pins the reviewer-role override semantics
// the orchestrator's runWithReviewPass used to own (issue #2975 slice 3),
// plus the fallback and partial-override cases that prove the two guards
// act independently (issue #2975 slice 6).
func TestMainRunRoleAwareModelEffort(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}

	handoff := promptassembly.Handoff{
		PromptFile:   promptFile,
		Model:        "opus",
		Effort:       "high",
		ReviewModel:  "sonnet",
		ReviewEffort: "low",
		ArgvShape:    claudeArgvShape,
	}

	cases := []struct {
		name         string
		role         string
		reviewModel  string
		reviewEffort string
		wantModel    string
		wantEffort   string
	}{
		{"implementor", "", handoff.ReviewModel, handoff.ReviewEffort, "opus", "high"},
		{"reviewer", driverkit.ReviewerRole, handoff.ReviewModel, handoff.ReviewEffort, "sonnet", "low"},
		// With both review overrides empty the fallback to Model/Effort is
		// role-independent (issue #2975 slice 6).
		{"reviewer-no-review-overrides", driverkit.ReviewerRole, "", "", "opus", "high"},
		{"reviewer-model-only", driverkit.ReviewerRole, handoff.ReviewModel, "", "sonnet", "high"},
		{"reviewer-effort-only", driverkit.ReviewerRole, "", handoff.ReviewEffort, "opus", "low"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			callLog := filepath.Join(dir, tc.name+"-calls.log")
			driverBin := writeFakeDriver(t, dir, "fake-driver-"+tc.name, "echo \"$@\" >> "+callLog+"\nexit 0\n")
			h := handoff
			h.DriverBin = driverBin
			h.ReviewModel = tc.reviewModel
			h.ReviewEffort = tc.reviewEffort
			handoffPath := writeHandoffFile(t, h)

			argv := []string{
				"-handoff-file", handoffPath,
				"-log-path", filepath.Join(dir, tc.name+"-stream.log"),
			}
			if tc.role != "" {
				argv = append(argv, "-top-level-role", tc.role)
			}

			var stdout, stderr bytes.Buffer
			rc := mainRun(argv, &stdout, &stderr)
			if rc != 0 {
				t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
			}

			calls, err := os.ReadFile(callLog)
			if err != nil {
				t.Fatalf("read callLog: %v", err)
			}
			got := strings.TrimSpace(string(calls))
			if !strings.Contains(got, "--model "+tc.wantModel) {
				t.Errorf("driver argv = %q, want it to contain \"--model %s\"", got, tc.wantModel)
			}
			if !strings.Contains(got, "--effort "+tc.wantEffort) {
				t.Errorf("driver argv = %q, want it to contain \"--effort %s\"", got, tc.wantEffort)
			}
		})
	}
}

// TestMainRunAnnouncesPassModel pins issue #6270: the pass's resolved model and
// effort reach stdout as a pass_model op, never the per-pass log (a log line
// would defeat run.go's empty-log check for a Driver that never started).
func TestMainRunAnnouncesPassModel(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	driverBin := writeFakeDriver(t, dir, "fake-driver", "exit 0\n")

	op := func(model, effort string) string {
		return hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: hostop.OpPassModel, Model: model, Effort: effort})
	}
	cases := []struct {
		name   string
		role   string
		model  string
		effort string
		want   string
	}{
		{"no-model", "", "", "", ""},
		{"implementor", "", "opus", "high", op("opus", "high")},
		{"reviewer", driverkit.ReviewerRole, "opus", "high", op("sonnet", "low")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(dir, tc.name+"-stream.log")
			handoffPath := writeHandoffFile(t, promptassembly.Handoff{
				PromptFile:   promptFile,
				DriverBin:    driverBin,
				Model:        tc.model,
				Effort:       tc.effort,
				ReviewModel:  "sonnet",
				ReviewEffort: "low",
				ArgvShape:    claudeArgvShape,
			})
			argv := []string{"-handoff-file", handoffPath, "-log-path", logPath}
			if tc.role != "" {
				argv = append(argv, "-top-level-role", tc.role)
			}
			var stdout, stderr bytes.Buffer
			if rc := mainRun(argv, &stdout, &stderr); rc != 0 {
				t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
			}
			if got := stdout.String(); got != tc.want {
				t.Errorf("stdout = %q, want %q", got, tc.want)
			}
			if log, err := os.ReadFile(logPath); err != nil || strings.Contains(string(log), "pass_model") {
				t.Errorf("log = %q (err %v), want no pass_model op", log, err)
			}
		})
	}
}

// TestMainRunReviewerEffortOpencodeShape pins issue #5350's driver-exec half:
// the reviewer role carries ReviewEffort through opencode's `--variant`.
func TestMainRunReviewerEffortOpencodeShape(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	callLog := filepath.Join(dir, "calls.log")
	driverBin := writeFakeDriver(t, dir, "fake-driver", "echo \"$@\" >> "+callLog+"\nexit 0\n")

	handoffPath := writeHandoffFile(t, promptassembly.Handoff{
		PromptFile:   promptFile,
		DriverBin:    driverBin,
		Model:        "anthropic/opus",
		Effort:       "high",
		ReviewModel:  "anthropic/sonnet",
		ReviewEffort: "low",
		ArgvShape:    opencodeArgvShape,
	})

	var stdout, stderr bytes.Buffer
	rc := mainRun([]string{
		"-handoff-file", handoffPath,
		"-log-path", filepath.Join(dir, "stream.log"),
		"-top-level-role", driverkit.ReviewerRole,
	}, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
	}
	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	got := strings.TrimSpace(string(calls))
	for _, want := range []string{"-m anthropic/sonnet", "--variant low"} {
		if !strings.Contains(got, want) {
			t.Errorf("driver argv = %q, want it to contain %q", got, want)
		}
	}
}

// TestMainRunPromptFileFallsBackToHandoff pins issue #2975 slice 3:
// -prompt-file still overrides the handoff, but is no longer required when
// the handoff already carries a PromptFile.
func TestMainRunPromptFileFallsBackToHandoff(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	driverBin := writeFakeDriver(t, dir, "fake-driver", "exit 0\n")

	handoffPath := writeHandoffFile(t, promptassembly.Handoff{
		PromptFile: promptFile,
		DriverBin:  driverBin,
		ArgvShape:  claudeArgvShape,
	})

	argv := []string{
		"-handoff-file", handoffPath,
		"-log-path", filepath.Join(dir, "stream.log"),
	}

	var stdout, stderr bytes.Buffer
	rc := mainRun(argv, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
	}
}

// TestMainRunPromptFileRequiredWhenBothEmpty pins that the handoff fallback
// did not drop the original "-prompt-file is required" error.
func TestMainRunPromptFileRequiredWhenBothEmpty(t *testing.T) {
	dir := t.TempDir()
	driverBin := writeFakeDriver(t, dir, "fake-driver", "exit 0\n")

	handoffPath := writeHandoffFile(t, promptassembly.Handoff{
		DriverBin: driverBin,
		ArgvShape: claudeArgvShape,
	})

	argv := []string{
		"-handoff-file", handoffPath,
		"-log-path", filepath.Join(dir, "stream.log"),
	}

	var stdout, stderr bytes.Buffer
	rc := mainRun(argv, &stdout, &stderr)
	if rc != 1 {
		t.Fatalf("mainRun exit code = %d, want 1", rc)
	}
	if !strings.Contains(stderr.String(), "-prompt-file is required") {
		t.Errorf("stderr = %q, want it to mention \"-prompt-file is required\"", stderr.String())
	}
}

// TestResolveExitAppliesOpencodeErrorEvent pins issue #2263: opencode exits 0
// after a mid-run error, so driver-exec must apply the Driver's ResolveExit
// result after run returns.
func TestResolveExitAppliesOpencodeErrorEvent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "stream.log")
	if err := os.WriteFile(logPath, []byte(`{"type":"error","error":"boom"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := driver.New("opencode")
	if err != nil {
		t.Fatalf("driver.New(opencode): %v", err)
	}

	if got := resolveExit(d, 0, logPath); got != 1 {
		t.Errorf("resolveExit = %d, want 1 (error event behind exit 0)", got)
	}
	if got := resolveExit(d, 7, logPath); got != 7 {
		t.Errorf("resolveExit = %d, want 7 (non-zero rc kept)", got)
	}
}

// TestResolveExitLeavesClaudeExitCodeUntouched pins the no-op case: claude's
// ResolveExit trusts the child rc, so resolveExit must not change it.
func TestResolveExitLeavesClaudeExitCodeUntouched(t *testing.T) {
	d, err := driver.New("claude")
	if err != nil {
		t.Fatalf("driver.New(claude): %v", err)
	}

	got := resolveExit(d, 7, filepath.Join(t.TempDir(), "does-not-exist.log"))
	if got != 7 {
		t.Errorf("resolveExit = %d, want 7 (claude's own rc, untouched)", got)
	}
}

// errResolveDriver is a Driver whose ResolveExit always fails.
type errResolveDriver struct{ driver.Driver }

func (errResolveDriver) ResolveExit(string, int) (int, error) {
	return 99, errors.New("resolve failed")
}

// A ResolveExit error must degrade to the child's own rc, not mask a possibly
// successful run behind the resolution failure.
func TestResolveExitDegradesToRCOnDriverError(t *testing.T) {
	for _, rc := range []int{0, 3} {
		if got := resolveExit(errResolveDriver{}, rc, "unused.log"); got != rc {
			t.Errorf("resolveExit(rc=%d) = %d, want the original rc", rc, got)
		}
	}
}

// A handoff without a HeartbeatLog still gets the coarse heartbeat at
// /tmp/heartbeat.log, the path a human tails inside the Box (issue #183).
func TestMainRunHeartbeatLogDefaultsToTmp(t *testing.T) {
	const defaultLog = "/tmp/heartbeat.log"
	os.Remove(defaultLog)
	t.Cleanup(func() { os.Remove(defaultLog) })

	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	driverBin := writeFakeDriver(t, dir, "fake-driver", `printf '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"main.go"}}]}}\n{"type":"result","num_turns":1}\n'`)
	handoffPath := writeHandoffFile(t, promptassembly.Handoff{
		PromptFile: promptFile,
		DriverBin:  driverBin,
		Issue:      "7",
		ArgvShape:  claudeArgvShape,
	})

	var stdout, stderr bytes.Buffer
	if rc := mainRun([]string{"-handoff-file", handoffPath, "-log-path", filepath.Join(dir, "stream.log")}, &stdout, &stderr); rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
	}
	got, err := os.ReadFile(defaultLog)
	if err != nil {
		t.Fatalf("read %s: %v", defaultLog, err)
	}
	if !bytes.Contains(got, []byte("#7")) {
		t.Errorf("heartbeat missing issue prefix: %q", got)
	}
}

// TestMainRunRoutesVerbs pins issue #6439: every verb reaches its own handler
// through mainRun's switch, and a verb word given only as a flag value does not.
func TestMainRunRoutesVerbs(t *testing.T) {
	cases := []struct {
		name       string
		argv       []string
		wantRC     int
		wantStdout string
		wantStderr string
		wantAbsent string
	}{
		{
			name:       "assemble-prompt",
			argv:       []string{"assemble-prompt"},
			wantRC:     1,
			wantStdout: "driver-exec assemble-prompt: -registry, -validate-markers-registry, -prompt-output, -agents-json-output, and -handoff-output are all required",
		},
		{
			name:       "bind-registry",
			argv:       []string{"bind-registry", "-intree-action", "bogus"},
			wantRC:     1,
			wantStdout: "driver-exec bind-registry: -intree-work-dir and -intree-action must be given together",
		},
		{
			name:       "probe-registry-socket",
			argv:       []string{"probe-registry-socket"},
			wantRC:     1,
			wantStdout: "driver-exec probe-registry-socket: -path is required",
		},
		{
			name:       "probe-registry-tcp",
			argv:       []string{"probe-registry-tcp"},
			wantRC:     1,
			wantStdout: "driver-exec probe-registry-tcp: -host is required",
		},
		{
			name:       "forward-registry-tcp",
			argv:       []string{"forward-registry-tcp"},
			wantRC:     1,
			wantStdout: "driver-exec forward-registry-tcp: -listen-port is required",
		},
		{
			name:       "verb name as a flag value falls through to the Driver path",
			argv:       []string{"--work-dir", "bind-registry"},
			wantRC:     2,
			wantStderr: "-work-dir",
			wantAbsent: "bind-registry:",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			rc := mainRun(tc.argv, &stdout, &stderr)
			if rc != tc.wantRC {
				t.Errorf("mainRun(%q) rc = %d, want %d", tc.argv, rc, tc.wantRC)
			}
			if tc.wantStdout != "" && !strings.Contains(stdout.String(), tc.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tc.wantStdout)
			}
			if tc.wantStderr != "" && !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tc.wantStderr)
			}
			if tc.wantAbsent != "" && strings.Contains(stdout.String()+stderr.String(), tc.wantAbsent) {
				t.Errorf("output %q%q must not contain %q", stdout.String(), stderr.String(), tc.wantAbsent)
			}
		})
	}
}

// TestMainRunAppliesHandoffDriverRows pins that driver-exec resolves the
// handoff's Driver after applying the rows file the handoff names. The file is
// the one the Nix renders for the fixture Driver Connector's row, so the
// Connector's row reaches an in-box binary through the real artifact.
func TestMainRunAppliesHandoffDriverRows(t *testing.T) {
	drivertest.Isolate(t)
	rows := os.Getenv("SPINDRIFT_FIXTURE_DRIVER_ROWS_FILE")
	if rows == "" {
		t.Skip("SPINDRIFT_FIXTURE_DRIVER_ROWS_FILE is unset")
	}
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("run the fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	handoffPath := writeHandoffFile(t, promptassembly.Handoff{
		PromptFile:     promptFile,
		Driver:         "fixture",
		DriverRowsFile: rows,
		DriverBin:      writeFakeDriver(t, dir, "fake-driver", "exit 0\n"),
		ArgvShape:      claudeArgvShape,
	})
	var stdout, stderr bytes.Buffer
	rc := mainRun([]string{"-handoff-file", handoffPath, "-log-path", filepath.Join(dir, "stream.log")}, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
	}
}

// shadowRun drives mainRun for the claude Driver over a fake CLI exiting rc,
// with rows (empty for none) as the handoff's Driver rows file, and returns the
// exit code and stdout.
func shadowRun(t *testing.T, rows string, rc int) (int, string) {
	t.Helper()
	return shadowRunDriver(t, "", rows, `{"type":"system","subtype":"init"}`, rc)
}

// shadowRunDriver is shadowRun for the named Driver, whose fake CLI writes
// logLine and exits rc.
func shadowRunDriver(t *testing.T, name, rows, logLine string, rc int) (int, string) {
	t.Helper()
	drivertest.Isolate(t)
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	writeTestFile(t, promptFile, "run the task")
	h := promptassembly.Handoff{
		PromptFile: promptFile,
		Driver:     name,
		DriverBin:  writeFakeDriver(t, dir, "fake-driver", `printf '%s\n' '`+logLine+`'; exit `+strconv.Itoa(rc)+"\n"),
		ArgvShape:  claudeArgvShape,
	}
	if rows != "" {
		h.DriverRowsFile = filepath.Join(dir, "rows.json")
		writeTestFile(t, h.DriverRowsFile, rows)
	}
	var stdout, stderr bytes.Buffer
	got := mainRun([]string{"-handoff-file", writeHandoffFile(t, h), "-log-path", filepath.Join(dir, "stream.log")}, &stdout, &stderr)
	return got, stdout.String()
}

func claudeShadowRow(t *testing.T, module string) string {
	t.Helper()
	return shadowRow(t, "claude", module)
}

func shadowRow(t *testing.T, name, module string) string {
	t.Helper()
	desc, ok := driver.DescriptorFor(name)
	if !ok {
		t.Fatalf("%s has no Descriptor", name)
	}
	b, err := json.Marshal(struct {
		driver.Descriptor
		Module string `json:"module"`
		Shadow bool   `json:"shadow"`
	}{desc, module, true})
	if err != nil {
		t.Fatal(err)
	}
	return "[" + string(b) + "]"
}

func TestMainRunWithoutShadowRowEmitsNoShadowOps(t *testing.T) {
	rc, out := shadowRun(t, "", 3)
	if rc != 3 {
		t.Errorf("exit code = %d, want 3", rc)
	}
	if strings.Contains(out, "shadow_") {
		t.Errorf("stdout carries a shadow op without a shadow row: %q", out)
	}
}

func shadowOps(t *testing.T, out string) (divs []hostop.ShadowDivergence, sum hostop.ShadowSummary) {
	t.Helper()
	var last hostop.SpindriftOp
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, `"shadow_`) {
			continue
		}
		if last.Op == hostop.OpShadowSummary {
			t.Fatalf("op after the shadow_summary: %s", line)
		}
		var ev hostop.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.SpindriftOp == nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		last = *ev.SpindriftOp
		if last.Op == hostop.OpShadowDivergence {
			divs = append(divs, *last.Divergence)
		}
	}
	if last.Op != hostop.OpShadowSummary || last.ShadowSummary == nil {
		t.Fatalf("no shadow_summary in stdout: %q", out)
	}
	return divs, *last.ShadowSummary
}

func TestMainRunShadowReportsDivergenceAndKeepsNativeExit(t *testing.T) {
	dir := os.Getenv("SPINDRIFT_CONNECTOR_FIXTURES_DIR")
	if dir == "" {
		t.Skip("SPINDRIFT_CONNECTOR_FIXTURES_DIR is unset")
	}
	rc, out := shadowRun(t, claudeShadowRow(t, filepath.Join(dir, "driver.wasm")), 3)
	if rc != 3 {
		t.Errorf("exit code = %d, want the native 3", rc)
	}
	divs, sum := shadowOps(t, out)
	if !sum.Loaded || sum.Error != "" || sum.Driver != "claude" || sum.Compared == 0 {
		t.Errorf("summary = %+v, want a clean claude replay", sum)
	}
	if len(divs) == 0 || divs[0].Line == "" || len(divs[0].Native) == 0 || len(divs[0].Shadow) == 0 {
		t.Errorf("divergences = %+v, want line with native and shadow results", divs)
	}
}

func TestMainRunShadowLoadFailureIsSummarisedNotFatal(t *testing.T) {
	garbage := filepath.Join(t.TempDir(), "garbage.wasm")
	writeTestFile(t, garbage, "not wasm")
	for name, module := range map[string]string{"missing": filepath.Join(t.TempDir(), "absent.wasm"), "garbage": garbage} {
		rc, out := shadowRun(t, claudeShadowRow(t, module), 3)
		if rc != 3 {
			t.Errorf("%s: exit code = %d, want 3", name, rc)
		}
		if _, sum := shadowOps(t, out); sum.Loaded || sum.Error == "" {
			t.Errorf("%s: summary = %+v, want Loaded=false with an Error", name, sum)
		}
	}
}

func TestMainRunMalformedShadowRowIsSummarisedNotFatal(t *testing.T) {
	rc, out := shadowRun(t, claudeShadowRow(t, ""), 3)
	if rc != 3 {
		t.Errorf("exit code = %d, want 3", rc)
	}
	_, sum := shadowOps(t, out)
	if sum.Loaded || !strings.Contains(sum.Error, "shadow row skipped") {
		t.Errorf("summary = %+v, want Loaded=false naming the skipped row", sum)
	}
	if sum.Driver != "claude" {
		t.Errorf("summary Driver = %q, want the defaulted name claude", sum.Driver)
	}
}

// The comparator's resolveExit check applies each side's correction to the
// CLI's raw code; handing it the already-corrected code would hide a Connector
// that does not port the correction.
func TestMainRunShadowHandsTheComparatorTheRawExitCode(t *testing.T) {
	module := filepath.Join(t.TempDir(), "driver.wasm")
	writeTestFile(t, module, "unused: the replay is stubbed")
	var got []int
	prev := shadowReplay
	shadowReplay = func(_ context.Context, _ driverkit.Strategy, _ []byte, _ string, exitCode int, _ func(hostop.SpindriftOp)) {
		got = append(got, exitCode)
	}
	t.Cleanup(func() { shadowReplay = prev })

	// opencode exits 0 after a mid-run error, which its ResolveExit turns into 1.
	rc, _ := shadowRunDriver(t, "opencode", shadowRow(t, "opencode", module), `{"type":"error","error":"boom"}`, 0)
	if rc != 1 {
		t.Fatalf("exit code = %d, want the native-corrected 1", rc)
	}
	if len(got) != 1 || got[0] != 0 {
		t.Errorf("comparator exit codes = %v, want the CLI's raw [0]", got)
	}
}
