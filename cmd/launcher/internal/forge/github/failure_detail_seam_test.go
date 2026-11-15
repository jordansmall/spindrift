package github

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/seamtest"
)

// Drives the real FailureDetail through the seam gh fake, so a fake that
// answers its contexts query with non-JSON fails here instead of in a stub.
func TestFailureDetailSeam(t *testing.T) {
	const url = "https://github.com/o/r/pull/9"
	tests := []struct {
		name      string
		pr        seamtest.GhPR
		wantEmpty bool
		want      []string
		wantNot   []string
	}{
		{
			name:      "no failing checks",
			pr:        seamtest.GhPR{Number: 9, URL: url, Checks: "SUCCESS"},
			wantEmpty: true,
		},
		{
			name: "failing check run",
			pr: seamtest.GhPR{Number: 9, URL: url, Checks: "FAILURE", Contexts: `[` +
				`{"__typename":"CheckRun","name":"build","conclusion":"FAILURE","summary":"boom"},` +
				`{"__typename":"CheckRun","name":"lint","conclusion":"SUCCESS","summary":"fine"}]`},
			want:    []string{"build", "boom"},
			wantNot: []string{"lint"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PATH", seamtest.InstallFakes(t, "gh")+":"+os.Getenv("PATH"))
			for k, v := range seamtest.WriteFakeConfig(t, "gh", seamtest.GhConfig{Record: filepath.Join(t.TempDir(), "gh.rec"), PRs: []seamtest.GhPR{tt.pr}}) {
				t.Setenv(k, v)
			}
			got, err := NewExecClient("o/r", testLabels).FailureDetail(url)
			if err != nil {
				t.Fatalf("FailureDetail: %v", err)
			}
			if tt.wantEmpty && got != "" {
				t.Errorf("got %q; want empty", got)
			}
			for _, s := range tt.want {
				if !strings.Contains(got, s) {
					t.Errorf("detail %q missing %q", got, s)
				}
			}
			for _, s := range tt.wantNot {
				if strings.Contains(got, s) {
					t.Errorf("detail %q contains %q", got, s)
				}
			}
		})
	}
}

// A gh that never answers the job-log reads must not stall FailureDetail past
// the shared log budget: the logs are dropped and the report still renders.
func TestFailureDetail_SlowJobLogsAreBoundedByTheBudget(t *testing.T) {
	const url = "https://github.com/o/r/pull/9"
	contexts := `[` +
		`{"__typename":"CheckRun","name":"build","conclusion":"FAILURE","summary":"boom","detailsUrl":"https://github.com/o/r/actions/runs/1/job/11"},` +
		`{"__typename":"CheckRun","name":"test","conclusion":"FAILURE","summary":"bang","detailsUrl":"https://github.com/o/r/actions/runs/1/job/12"}]`
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$2\" in\ngraphql) printf '%s' '" + contexts + "' ;;\n*) exec sleep 30 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	prev := jobLogsBudget
	jobLogsBudget = 200 * time.Millisecond
	t.Cleanup(func() { jobLogsBudget = prev })

	start := time.Now()
	got, err := NewExecClient("o/r", testLabels).FailureDetail(url)
	if err != nil {
		t.Fatalf("FailureDetail: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("FailureDetail took %v; want it bounded by the %v log budget", elapsed, jobLogsBudget)
	}
	for _, s := range []string{"build", "boom", "test", "bang"} {
		if !strings.Contains(got, s) {
			t.Errorf("detail %q missing %q", got, s)
		}
	}
}

// A failed nix check prints its decisive error and the FAIL lines mid-log,
// then more than the tail keeps (issue #7084); the detail still carries them.
func TestFailureDetail_NixCheckLogKeepsTheErrorBehindTheTail(t *testing.T) {
	const url = "https://github.com/o/r/pull/9"
	contexts := `[{"__typename":"CheckRun","name":"checks","conclusion":"FAILURE","summary":"",` +
		`"detailsUrl":"https://github.com/o/r/actions/runs/1/job/11"}]`
	dir := t.TempDir()
	logPath, err := filepath.Abs(filepath.Join("testdata", "job-log-nix-check-7016.txt"))
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$2\" in\ngraphql) printf '%s' '" + contexts + "' ;;\n*) cat '" + logPath + "' ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	got, err := NewExecClient("o/r", testLabels).FailureDetail(url)
	if err != nil {
		t.Fatalf("FailureDetail: %v", err)
	}
	if len(got) > forge.MaxFailureDetailBytes {
		t.Errorf("detail is %d bytes, want at most %d", len(got), forge.MaxFailureDetailBytes)
	}
	for _, s := range []string{
		"error: experimental Nix feature 'nix-command' is disabled",
		"--- FAIL: TestSeamNixEval",
		"Cleaning up orphan processes",
	} {
		if !strings.Contains(got, s) {
			t.Errorf("detail missing %q:\n%s", s, got)
		}
	}
}

// #7016 failed on three platform legs at once: each excerpt gets a third of
// the budget, and its error-line half shrinks with it.
func TestFailureDetail_NixCheckLogOnThreeLegsKeepsEachLegsError(t *testing.T) {
	const url = "https://github.com/o/r/pull/9"
	legs := []string{"checks (x86_64-linux)", "checks (aarch64-linux)", "checks (aarch64-darwin)"}
	var runs []string
	for i, leg := range legs {
		runs = append(runs, `{"__typename":"CheckRun","name":"`+leg+`","conclusion":"FAILURE","summary":"",`+
			`"detailsUrl":"https://github.com/o/r/actions/runs/1/job/`+strconv.Itoa(11+i)+`"}`)
	}
	contexts := "[" + strings.Join(runs, ",") + "]"
	dir := t.TempDir()
	logPath, err := filepath.Abs(filepath.Join("testdata", "job-log-nix-check-7016.txt"))
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$2\" in\ngraphql) printf '%s' '" + contexts + "' ;;\n*) cat '" + logPath + "' ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	got, err := NewExecClient("o/r", testLabels).FailureDetail(url)
	if err != nil {
		t.Fatalf("FailureDetail: %v", err)
	}
	if len(got) > forge.MaxFailureDetailBytes {
		t.Errorf("detail is %d bytes, want at most %d", len(got), forge.MaxFailureDetailBytes)
	}
	sections := strings.Split(got, "---\n")
	if len(sections) < len(legs) {
		t.Fatalf("detail has %d sections, want one per leg:\n%s", len(sections), got)
	}
	for i, leg := range legs {
		section := sections[i]
		if !strings.HasPrefix(section, leg+": ") {
			t.Fatalf("section %d does not open with leg %q:\n%s", i, leg, section)
		}
		for _, s := range []string{
			"error: experimental Nix feature 'nix-command' is disabled",
			"--- FAIL: TestSeamNixEval",
		} {
			if !strings.Contains(section, s) {
				t.Errorf("leg %q missing %q:\n%s", leg, s, section)
			}
		}
	}
}
