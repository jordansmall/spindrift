package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsBindRegistryInvocation verifies the bind-registry subcommand's
// dispatch guard: a bare "bind-registry" first arg selects it, while every
// other invocation shape falls through to a different path, mirroring
// TestIsReadonlyGuardsInvocation/TestIsMarkerGateInvocation.
func TestIsBindRegistryInvocation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"bind-registry first arg", []string{"bind-registry"}, true},
		{"bind-registry with flags", []string{"bind-registry", "--work-dir", "x"}, true},
		{"no args", nil, false},
		{"other", []string{"other"}, false},
		{"flag names bind-registry as a value, not args[0]", []string{"--work-dir", "bind-registry"}, false},
	}
	for _, c := range cases {
		if got := isBindRegistryInvocation(c.args); got != c.want {
			t.Errorf("%s: isBindRegistryInvocation(%v) = %v, want %v", c.name, c.args, got, c.want)
		}
	}
}

// TestRunBindRegistry_WritesClassification verifies runBindRegistry calls
// bindregistry.Classify against -work-dir and writes the classification into
// -ecosystem-env-output as a sourceable NUDGE_ECOSYSTEM assignment.
func TestRunBindRegistry_WritesClassification(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "Cargo.lock"), []byte(""), 0o644); err != nil {
		t.Fatalf("write Cargo.lock: %v", err)
	}
	envOut := filepath.Join(t.TempDir(), "nudge.env")

	var stdout bytes.Buffer
	rc := runBindRegistry([]string{
		"-work-dir", workDir,
		"-ecosystem-env-output", envOut,
	}, &stdout)
	if rc != 0 {
		t.Fatalf("runBindRegistry exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatalf("read ecosystem env output: %v", err)
	}
	want := "NUDGE_ECOSYSTEM=\"cargo\"\n"
	if string(got) != want {
		t.Errorf("ecosystem env output = %q, want %q", got, want)
	}
}

// TestRunBindRegistry_NoLockfileWritesEmptyClassification verifies a
// work-dir with no recognized lockfile writes an empty NUDGE_ECOSYSTEM
// assignment rather than erroring.
func TestRunBindRegistry_NoLockfileWritesEmptyClassification(t *testing.T) {
	workDir := t.TempDir()
	envOut := filepath.Join(t.TempDir(), "nudge.env")

	var stdout bytes.Buffer
	rc := runBindRegistry([]string{
		"-work-dir", workDir,
		"-ecosystem-env-output", envOut,
	}, &stdout)
	if rc != 0 {
		t.Fatalf("runBindRegistry exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatalf("read ecosystem env output: %v", err)
	}
	want := "NUDGE_ECOSYSTEM=\"\"\n"
	if string(got) != want {
		t.Errorf("ecosystem env output = %q, want %q", got, want)
	}
}

// TestRunBindRegistry_MissingFlagsErrors verifies both -work-dir and
// -ecosystem-env-output are required: omitting either fails loudly (exit 1)
// instead of running Classify against a zero-value work dir or silently
// discarding the classification.
func TestRunBindRegistry_MissingFlagsErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"missing both", nil},
		{"missing ecosystem-env-output", []string{"-work-dir", t.TempDir()}},
		{"missing work-dir", []string{"-ecosystem-env-output", filepath.Join(t.TempDir(), "nudge.env")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout bytes.Buffer
			rc := runBindRegistry(c.args, &stdout)
			if rc == 0 {
				t.Fatalf("runBindRegistry exit = 0, want non-zero for %v", c.args)
			}
		})
	}
}

// TestRunBindRegistry_WriteFailureReturnsNonZero verifies runBindRegistry
// surfaces an os.WriteFile failure on -ecosystem-env-output (rather than a
// panic or a silent success): pointing the output at a path whose parent
// directory doesn't exist forces WriteFile to fail past the Classify call,
// which can no longer itself return an error.
func TestRunBindRegistry_WriteFailureReturnsNonZero(t *testing.T) {
	workDir := t.TempDir()
	envOut := filepath.Join(t.TempDir(), "nonexistent-subdir", "nudge.env")

	var stdout bytes.Buffer
	rc := runBindRegistry([]string{
		"-work-dir", workDir,
		"-ecosystem-env-output", envOut,
	}, &stdout)
	if rc == 0 {
		t.Fatalf("runBindRegistry exit = 0, want non-zero (stdout=%q)", stdout.String())
	}
	if !strings.Contains(stdout.String(), "write ecosystem env output") {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), "write ecosystem env output")
	}
}
