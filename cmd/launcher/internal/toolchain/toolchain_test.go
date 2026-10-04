package toolchain

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type nixCall struct {
	dir  string
	args []string
}

// fakeNix records its calls and returns fn's result (nil fn means success).
func fakeNix(calls *[]nixCall, fn func(ctx context.Context) error) Nix {
	return func(ctx context.Context, dir string, args ...string) error {
		*calls = append(*calls, nixCall{dir, args})
		if fn == nil {
			return nil
		}
		return fn(ctx)
	}
}

func writeFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDecide_Probe(t *testing.T) {
	const (
		found  = "==> devShell found — lifecycle will run inside nix develop"
		absent = "==> no devShell in flake (or nix develop failed) — using baked toolchain"
		head   = "==> flake.nix found in cloned repo; probing for devShell"
	)
	tests := []struct {
		name      string
		flake     bool
		timeout   string
		fn        func(ctx context.Context) error
		want      Probe
		wantCalls int
		wantLine  string
	}{
		{name: "no flake", timeout: "300", want: NoFlake},
		{name: "flake without devshell", flake: true, timeout: "300",
			fn:   func(context.Context) error { return errors.New("exit status 1") },
			want: Absent, wantCalls: 1, wantLine: absent},
		{name: "devshell present", flake: true, timeout: "300",
			want: Found, wantCalls: 1, wantLine: found},
		{name: "probe times out", flake: true, timeout: "0.05",
			fn:   func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
			want: TimedOut, wantCalls: 1,
			wantLine: "==> devShell probe timed out (0.05s) — using baked toolchain"},
		{name: "nix missing", flake: true, timeout: "300",
			fn:   func(context.Context) error { return exec.ErrNotFound },
			want: Absent, wantCalls: 1, wantLine: absent},
		{name: "unparsable timeout", flake: true, timeout: "soon",
			want: Absent, wantLine: absent},
		{name: "negative timeout", flake: true, timeout: "-1",
			want: Absent, wantLine: absent},
		{name: "minutes suffix", flake: true, timeout: "5m",
			want: Found, wantCalls: 1, wantLine: found},
		{name: "fractional hours suffix", flake: true, timeout: "1.5h",
			want: Found, wantCalls: 1, wantLine: found},
		{name: "seconds suffix", flake: true, timeout: "30s",
			want: Found, wantCalls: 1, wantLine: found},
		{name: "days suffix", flake: true, timeout: "1d",
			want: Found, wantCalls: 1, wantLine: found},
		{name: "suffix alone", flake: true, timeout: "m",
			want: Absent, wantLine: absent},
		{name: "negative with suffix", flake: true, timeout: "-5m",
			want: Absent, wantLine: absent},
		{name: "empty timeout", flake: true, timeout: "",
			want: Absent, wantLine: absent},
		{name: "zero timeout is unbounded", flake: true, timeout: "0",
			want: Found, wantCalls: 1, wantLine: found},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.flake {
				writeFile(t, dir, "flake.nix")
			}
			var calls []nixCall
			var out strings.Builder
			var headBeforeNix bool
			d := Decide(context.Background(), &out, dir, "ci", tc.timeout, "", fakeNix(&calls, func(ctx context.Context) error {
				headBeforeNix = out.String() == head+"\n"
				if tc.fn == nil {
					return nil
				}
				return tc.fn(ctx)
			}))
			wantOut := ""
			if tc.flake {
				wantOut = head + "\n"
			}
			if out.String() != wantOut {
				t.Errorf("stdout = %q, want %q", out.String(), wantOut)
			}
			if tc.wantCalls == 1 && !headBeforeNix {
				t.Error("probing line was not written before nix ran")
			}
			if d.Probe != tc.want {
				t.Errorf("Probe = %v, want %v", d.Probe, tc.want)
			}
			if d.Devshell() != (tc.want == Found) {
				t.Errorf("Devshell() = %v for probe %v", d.Devshell(), d.Probe)
			}
			if len(calls) != tc.wantCalls {
				t.Fatalf("nix called %d times, want %d", len(calls), tc.wantCalls)
			}
			if tc.wantCalls == 1 {
				want := nixCall{dir, []string{"develop", ".#ci", "--command", "true"}}
				if !reflect.DeepEqual(calls[0], want) {
					t.Errorf("nix call = %+v, want %+v", calls[0], want)
				}
			}
			if got := d.Line(); got != tc.wantLine {
				t.Errorf("Line() = %q, want %q", got, tc.wantLine)
			}
		})
	}
}

func TestDecide_UnboundedTimeoutHasNoDeadline(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "flake.nix")
	var hasDeadline bool
	var calls []nixCall
	Decide(context.Background(), io.Discard, dir, "default", "0", "", fakeNix(&calls, func(ctx context.Context) error {
		_, hasDeadline = ctx.Deadline()
		return nil
	}))
	if hasDeadline {
		t.Error("timeout 0 bounded the probe context")
	}
}

func TestDecide_Name(t *testing.T) {
	var calls []nixCall
	if d := Decide(context.Background(), io.Discard, t.TempDir(), "", "300", "", fakeNix(&calls, nil)); d.Name != "default" {
		t.Errorf("Name = %q, want default", d.Name)
	}
	if d := Decide(context.Background(), io.Discard, t.TempDir(), "ci", "300", "", fakeNix(&calls, nil)); d.Name != "ci" {
		t.Errorf("Name = %q, want ci", d.Name)
	}
}

func TestDecide_AndHint_Ecosystem(t *testing.T) {
	const goHint = "==> hint: go mod project detected; set 'prefetch' to warm dependency caches per run, or 'packages' to bake a toolchain into the image"
	tests := []struct {
		file     string
		wantEco  string
		wantHint bool
	}{
		{"go.sum", "go mod", true},
		{"build.gradle.kts", "gradle", true},
		{"build.gradle", "gradle", true},
		{"settings.gradle", "gradle", true},
		{"settings.gradle.kts", "gradle", true},
		{"gradle.lockfile", "gradle", true},
		{"package-lock.json", "npm/pnpm/yarn", true},
		{"yarn.lock", "npm/pnpm/yarn", true},
		{"pnpm-lock.yaml", "npm/pnpm/yarn", true},
		{"README.md", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, tc.file)
			var calls []nixCall
			d := Decide(context.Background(), io.Discard, dir, "default", "300", "", fakeNix(&calls, nil))
			if d.Ecosystem != tc.wantEco {
				t.Fatalf("Ecosystem = %q, want %q", d.Ecosystem, tc.wantEco)
			}
			want := ""
			if tc.wantHint {
				want = "==> hint: " + tc.wantEco + " project detected; set 'prefetch' to warm dependency caches per run, or 'packages' to bake a toolchain into the image"
			}
			if got := d.Hint(""); got != want {
				t.Errorf("Hint(\"\") = %q, want %q", got, want)
			}
			if got := d.Hint("go mod download"); got != "" {
				t.Errorf("Hint with prefetch set = %q, want empty", got)
			}
		})
	}
	d := Decision{Ecosystem: "go mod"}
	if got := d.Hint(""); got != goHint {
		t.Errorf("Hint = %q, want %q", got, goHint)
	}
}

func TestPrefetchCmd_Args(t *testing.T) {
	if cmd := (Decision{}).PrefetchCmd("", "/w", "/h"); cmd != nil {
		t.Errorf("empty hook gave %v, want nil", cmd.Args)
	}

	cmd := (Decision{Probe: Absent}).PrefetchCmd("go mod download", "/w", "/h")
	if want := []string{"bash", "-c", "go mod download"}; !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("non-devshell args = %q, want %q", cmd.Args, want)
	}
	if cmd.Dir != "/w" {
		t.Errorf("Dir = %q, want /w", cmd.Dir)
	}
	for _, kv := range []string{"WORK_DIR=/w", "PREFETCH=go mod download"} {
		if !slices.Contains(cmd.Env, kv) {
			t.Errorf("Env missing %q", kv)
		}
	}

	cmd = (Decision{Probe: Found, Name: "ci"}).PrefetchCmd("go mod download", "/w", "/h/bin")
	want := []string{"nix", "develop", ".#ci", "--command", "bash", "-c",
		`export PATH="$1:$PATH"; eval "$PREFETCH"`, "prefetch", "/h/bin"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("devshell args = %q, want %q", cmd.Args, want)
	}
	if cmd.Dir != "/w" {
		t.Errorf("Dir = %q, want /w", cmd.Dir)
	}
}

func TestPrefetchCmd_RunsHookInChildBash(t *testing.T) {
	caller, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	out := filepath.Join(t.TempDir(), "out")
	hook := `mkdir -p sub && cd sub; printf '%s|%s' "$WORK_DIR" "$PWD" > ` + out + `; exit 0`
	cmd := (Decision{Probe: Absent}).PrefetchCmd(hook, work, "/h")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if now, _ := os.Getwd(); now != caller {
		t.Errorf("hook moved the caller's cwd to %q", now)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	wantDir, _ := filepath.EvalSymlinks(filepath.Join(work, "sub"))
	gotParts := strings.SplitN(string(got), "|", 2)
	gotPwd, _ := filepath.EvalSymlinks(gotParts[1])
	if gotParts[0] != work || gotPwd != wantDir {
		t.Errorf("hook saw WORK_DIR|PWD = %q, want %q|%q", got, work, wantDir)
	}
}

func TestPrefetchCmd_FailingHookReturnsError(t *testing.T) {
	cmd := (Decision{Probe: Absent}).PrefetchCmd("exit 3", t.TempDir(), "/h")
	var ee *exec.ExitError
	if err := cmd.Run(); !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Errorf("Run() = %v, want exit 3", err)
	}
}

func TestPrefetchCmd_DevshellPrependsHarnessPathAndEvalsHook(t *testing.T) {
	bin := t.TempDir()
	// Stands in for `nix develop .#x --command <rest>`: drops those four words.
	script := "#!/bin/sh\nshift 3\nexec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "nix"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	out := filepath.Join(t.TempDir(), "path")
	hook := `printf '%s' "$PATH" > ` + out
	cmd := (Decision{Probe: Found, Name: "x"}).PrefetchCmd(hook, t.TempDir(), "/harness/bin")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	if !strings.HasPrefix(string(got), "/harness/bin:") {
		t.Errorf("PATH in hook = %q, want /harness/bin first", got)
	}
}

func TestRunNix(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\necho \"$PWD $*\"; echo hidden >&2\n"
	if err := os.WriteFile(filepath.Join(bin, "nix"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	dir := t.TempDir()
	var out strings.Builder
	if err := RunNix(&out)(context.Background(), dir, "develop", ".#x"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.TrimSpace(out.String()), " develop .#x") || strings.Contains(out.String(), "hidden") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestRunNix_ContextBoundsHungNix(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "nix"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	d := Decide(context.Background(), io.Discard, flakeDir(t), "default", "0.2", "", RunNix(nil))
	if d.Probe != TimedOut {
		t.Errorf("Probe = %v, want TimedOut", d.Probe)
	}
}

func flakeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "flake.nix")
	return dir
}

func TestDecide_HintPrecedesProbingLine(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "flake.nix")
	writeFile(t, dir, "go.sum")
	var out strings.Builder
	var calls []nixCall
	Decide(context.Background(), &out, dir, "default", "300", "", fakeNix(&calls, nil))
	want := "==> hint: go mod project detected; set 'prefetch' to warm dependency caches per run, or 'packages' to bake a toolchain into the image\n" +
		"==> flake.nix found in cloned repo; probing for devShell\n"
	if out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}

	out.Reset()
	Decide(context.Background(), &out, dir, "default", "300", "go mod download", fakeNix(&calls, nil))
	if strings.Contains(out.String(), "hint:") {
		t.Errorf("stdout = %q, want no hint with prefetch set", out.String())
	}
}

func TestPrefetchWarning(t *testing.T) {
	want := "==> WARNING: prefetch hook failed (exit status 1) — continuing"
	if got := PrefetchWarning(errors.New("exit status 1")); got != want {
		t.Errorf("PrefetchWarning = %q, want %q", got, want)
	}
}
