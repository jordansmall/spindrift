package seamtest

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type driverCall struct {
	argv []string
	out  string
	exit int
}

// runOrchestrator drives the fake with a stub Driver and returns what the
// orchestrator exited with and wrote to stdout, plus the stub's calls.
func runOrchestrator(t *testing.T, cfg OrchestratorConfig, drv driverCall, args ...string) (code int, stdout string, calls []driverCall) {
	t.Helper()
	for k, v := range WriteFakeConfig(t, "orchestrator", cfg) {
		t.Setenv(k, v)
	}
	var out, errb bytes.Buffer
	code = orchestratorFake(args, &out, &errb, func(argv []string, w io.Writer, _ io.Writer) int {
		calls = append(calls, driverCall{argv: argv})
		io.WriteString(w, drv.out)
		return drv.exit
	})
	return code, out.String(), calls
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOrchestratorRegistered(t *testing.T) {
	if _, ok := fakes["orchestrator"]; !ok {
		t.Fatal("no orchestrator fake registered")
	}
}

func TestOrchestratorInvokesDriverWithSessionFlagsThenPrompt(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "rec")
	prompt := writeFile(t, "finish up\nplease")
	session := writeFile(t, "--resume abc-123\n")
	logPath := filepath.Join(t.TempDir(), "log")
	args := []string{"--handoff-file", "/h.json", "--prompt-file", prompt, "--session-file", session, "--log-path", logPath, "--manifest-path", "/o/manifest.json"}

	code, out, calls := runOrchestrator(t, OrchestratorConfig{Record: rec}, driverCall{out: "driver says\n", exit: 4}, args...)

	if code != 4 || out != "driver says\n" {
		t.Errorf("exit %d, stdout %q; want 4, %q", code, out, "driver says\n")
	}
	wantArgv := []string{"--resume", "abc-123", "-p", "finish up\nplease"}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0].argv, wantArgv) {
		t.Errorf("driver calls = %v; want one with %v", calls, wantArgv)
	}
	if b, _ := os.ReadFile(logPath); string(b) != "driver says\n" {
		t.Errorf("log = %q; want the driver's stdout teed", b)
	}
	if got := ReadRecord(t, rec); !reflect.DeepEqual(got, [][]string{args}) {
		t.Errorf("record = %v; want the full argv, ignored flags included", got)
	}
}

func TestOrchestratorEmptySessionFileAddsNoFlags(t *testing.T) {
	prompt := writeFile(t, "p")
	logPath := filepath.Join(t.TempDir(), "log")
	_, _, calls := runOrchestrator(t, OrchestratorConfig{Record: filepath.Join(t.TempDir(), "rec")}, driverCall{},
		"--prompt-file", prompt, "--session-file", writeFile(t, ""), "--log-path", logPath)
	if want := []string{"-p", "p"}; len(calls) != 1 || !reflect.DeepEqual(calls[0].argv, want) {
		t.Errorf("driver calls = %v; want one with %v", calls, want)
	}
}

func TestOrchestratorRequiresItsFlags(t *testing.T) {
	var out, errb bytes.Buffer
	t.Setenv(configEnv("orchestrator"), WriteFakeConfig(t, "orchestrator", OrchestratorConfig{Record: filepath.Join(t.TempDir(), "rec")})[configEnv("orchestrator")])
	code := orchestratorFake([]string{"--prompt-file", "/p"}, &out, &errb, func([]string, io.Writer, io.Writer) int {
		t.Error("driver ran without --session-file and --log-path")
		return 0
	})
	if code != 2 {
		t.Errorf("exit %d; want 2", code)
	}
}

func TestOrchestratorMissingConfigExits(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv(configEnv("orchestrator"), "")
	var out, errb bytes.Buffer
	if code := orchestratorFake(nil, &out, &errb, nil); code != fakeConfigExit {
		t.Errorf("exit %d; want %d", code, fakeConfigExit)
	}
}
