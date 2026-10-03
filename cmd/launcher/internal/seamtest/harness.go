package seamtest

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

const launcherModule = "module spindrift.dev/launcher"

// findModuleRoot returns the nearest ancestor of start (inclusive) whose
// go.mod declares the launcher module.
func findModuleRoot(start string) (string, error) {
	for dir := start; ; {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.TrimSpace(line) == launcherModule {
					return dir, nil
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %q go.mod found above %s", launcherModule, start)
		}
		dir = parent
	}
}

// Build compiles pkg (a path relative to the launcher module root, e.g. "."
// or "./cmd/foo") into tb's temp dir and returns the binary's path. GOPROXY=off
// makes a missing module fail instead of reaching the network; Go picks up
// vendor/ on its own when the tree has one. Each call links afresh -- the Go
// build cache makes repeats cheap, and a shared output dir would outlive tb.
func Build(tb testing.TB, pkg string) string {
	tb.Helper()
	wd, err := os.Getwd()
	if err != nil {
		tb.Fatalf("seamtest: %v", err)
	}
	root, err := findModuleRoot(wd)
	if err != nil {
		tb.Fatalf("seamtest: %v", err)
	}
	name := path.Base(path.Clean(pkg))
	if name == "." {
		name = path.Base(filepath.ToSlash(root))
	}
	out := filepath.Join(tb.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOPROXY=off")
	if b, err := cmd.CombinedOutput(); err != nil {
		tb.Fatalf("seamtest: go build %s: %v\n%s", pkg, err, b)
	}
	return out
}

// BuildLauncher compiles the launcher binary itself; see Build.
func BuildLauncher(tb testing.TB) string {
	tb.Helper()
	return Build(tb, ".")
}

// Cmd describes one subprocess run.
type Cmd struct {
	Bin  string
	Args []string
	// Env overlays the parent environment; an empty value sets the variable
	// empty rather than unsetting it.
	Env map[string]string
	// CleanEnv drops the parent environment except TMPDIR, so ambient knob
	// variables (a dev shell or Box exports many) cannot leak into a program
	// whose config is meant to come from its input document alone.
	CleanEnv bool
	// PathDirs are prepended to the parent PATH, in order.
	PathDirs []string
	// Dir is the working directory; empty inherits the parent's.
	Dir string
}

// Result is a finished subprocess's captured output.
type Result struct {
	Stdout, Stderr string
	ExitCode       int
}

// Run executes c with a fresh temp HOME and PATH = PathDirs + parent PATH.
// The rest of the parent environment is inherited (unless CleanEnv) so the Go toolchain and
// nix-sandbox settings stay usable. Both streams are also logged to tb so a
// failing test shows what the child said. A non-zero exit is a Result, not a
// failure; only a failure to start fails tb.
func Run(tb testing.TB, c Cmd) Result {
	tb.Helper()
	pathVal := strings.Join(append(append([]string{}, c.PathDirs...), os.Getenv("PATH")), string(os.PathListSeparator))
	base := os.Environ()
	if c.CleanEnv {
		base = []string{"TMPDIR=" + os.TempDir()}
	}
	env := append(base, "HOME="+tb.TempDir(), "PATH="+pathVal)
	for k, v := range c.Env {
		env = append(env, k+"="+v)
	}
	cmd := exec.Command(c.Bin, c.Args...)
	cmd.Dir = c.Dir
	// Later duplicates win in os/exec, so the overlay order above is the
	// precedence order.
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	res := Result{}
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			tb.Fatalf("seamtest: run %s: %v", c.Bin, err)
		}
		res.ExitCode = ee.ExitCode()
	}
	res.Stdout, res.Stderr = stdout.String(), stderr.String()
	tb.Logf("seamtest: %s exit=%d\nstdout:\n%s\nstderr:\n%s", c.Bin, res.ExitCode, res.Stdout, res.Stderr)
	return res
}

// WantExit fails tb, showing both streams, unless the exit code is code.
func (r Result) WantExit(tb testing.TB, code int) {
	tb.Helper()
	if r.ExitCode != code {
		tb.Fatalf("exit code = %d; want %d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, code, r.Stdout, r.Stderr)
	}
}
