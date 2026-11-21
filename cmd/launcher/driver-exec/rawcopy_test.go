package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/shadow"
	"spindrift.dev/launcher/internal/hostop"
)

// isolatedTmp points the temp dir at an empty one, so a leaked raw copy shows.
func isolatedTmp(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	return tmp
}

func tmpEntries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// stubReplay swaps shadowReplay for one that records the raw path it was given
// and the bytes there during the call.
func stubReplay(t *testing.T, calls *[]string, raws *[]string) {
	t.Helper()
	prev := shadowReplay
	shadowReplay = func(_ context.Context, _ driverkit.Strategy, _ shadow.Loader, _ []byte, _, rawPath string, _ int, _ func(hostop.SpindriftOp)) {
		*calls = append(*calls, rawPath)
		b, _ := os.ReadFile(rawPath)
		*raws = append(*raws, string(b))
	}
	t.Cleanup(func() { shadowReplay = prev })
}

func piShadowRow(t *testing.T) string {
	t.Helper()
	module := filepath.Join(t.TempDir(), "driver.wasm")
	writeTestFile(t, module, "unused: the replay is stubbed")
	return shadowRow(t, "pi", module)
}

// The filter shadow replays the CLI's stdout, which the Pass log (the native
// filter's output) cannot rebuild; the copy is deleted once the Pass ends.
func TestMainRunShadowedPiKeepsARawStdoutCopyOnlyForTheReplay(t *testing.T) {
	tmp := isolatedTmp(t)
	var paths, raws []string
	stubReplay(t, &paths, &raws)

	// The native filter holds this delta back, so the log lacks it and only the
	// raw copy carries it.
	delta := strings.TrimSuffix(piDeltaHel, "\n")
	var stdout bytes.Buffer
	if rc := shadowRunDriverTo(t, "pi", piShadowRow(t), delta, 0, &stdout); rc != 0 {
		t.Fatalf("exit code = %d, want 0", rc)
	}
	if len(paths) != 1 || paths[0] == "" {
		t.Fatalf("raw paths handed to the replay = %q, want one non-empty", paths)
	}
	if raws[0] != piDeltaHel {
		t.Errorf("raw copy during the replay = %q, want the CLI's stdout %q", raws[0], piDeltaHel)
	}
	if _, err := os.Stat(paths[0]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("raw copy after the Pass: stat err = %v, want it gone", err)
	}
	if left := tmpEntries(t, tmp); len(left) != 0 {
		t.Errorf("temp dir holds %v after the Pass, want it empty", left)
	}
}

// A truncated Pass is summarised without a replay, and still cleans up.
func TestMainRunShadowedPiRunFailureRemovesTheRawCopy(t *testing.T) {
	tmp := isolatedTmp(t)
	var out failFirstWrite
	if rc := shadowRunDriverTo(t, "pi", piShadowRow(t), strings.TrimSuffix(piMessageStart, "\n"), 0, &out); rc != 1 {
		t.Fatalf("exit code = %d, want 1", rc)
	}
	if left := tmpEntries(t, tmp); len(left) != 0 {
		t.Errorf("temp dir holds %v after a failed Pass, want it empty", left)
	}
}

// The raw copy is a side channel: the live path's stdout and Pass log are the
// same bytes with the filter shadow on as off.
func TestMainRunShadowedPiLeavesTheLivePathByteIdentical(t *testing.T) {
	isolatedTmp(t)
	var paths, raws []string
	stubReplay(t, &paths, &raws)
	line := strings.TrimSuffix(piMessageStart+piDeltaHel, "\n")

	var offOut, onOut bytes.Buffer
	_, offLog := shadowRunDriverLog(t, "pi", "", line, 0, &offOut, &bytes.Buffer{})
	_, onLog := shadowRunDriverLog(t, "pi", piShadowRow(t), line, 0, &onOut, &bytes.Buffer{})
	if len(paths) != 1 || paths[0] == "" {
		t.Fatalf("raw paths handed to the replay = %q, want one non-empty (the shadow ran)", paths)
	}
	if !bytes.Equal(offOut.Bytes(), onOut.Bytes()) {
		t.Errorf("stdout differs:\n off %q\n  on %q", offOut.String(), onOut.String())
	}
	off, err := os.ReadFile(offLog)
	if err != nil {
		t.Fatal(err)
	}
	on, err := os.ReadFile(onLog)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(off, on) {
		t.Errorf("Pass log differs:\n off %q\n  on %q", off, on)
	}
}

// With no shadow in play the live path gains no writer and no file.
func TestMainRunWithoutAFilterShadowKeepsNoRawCopy(t *testing.T) {
	line := strings.TrimSuffix(piDeltaHel, "\n")
	moduleless := shadowRow(t, "pi", "")
	module := filepath.Join(t.TempDir(), "driver.wasm")
	writeTestFile(t, module, "unused: the replay is stubbed")
	for name, c := range map[string]struct{ driver, rows string }{
		"no shadow row":           {"pi", ""},
		"skipped shadow row":      {"pi", moduleless},
		"native without a filter": {"opencode", shadowRow(t, "opencode", module)},
		"unshadowed other Driver": {"opencode", ""},
	} {
		t.Run(name, func(t *testing.T) {
			tmp := isolatedTmp(t)
			var paths, raws []string
			stubReplay(t, &paths, &raws)
			var stdout bytes.Buffer
			rc := shadowRunDriverTo(t, c.driver, c.rows, line, 0, &stdout)
			for _, p := range paths {
				if p != "" {
					t.Errorf("replay got raw path %q, want none", p)
				}
			}
			if left := tmpEntries(t, tmp); len(left) != 0 {
				t.Errorf("temp dir holds %v, want no raw copy", left)
			}
			if rc != 0 {
				t.Errorf("exit code = %d, want 0", rc)
			}
		})
	}
}

// A raw-copy write that fails must reach neither the CLI's stdout pipe nor the
// run, and the replay must then be told there is no copy.
func TestRunSurvivesARawCopyWriteFailure(t *testing.T) {
	isolatedTmp(t)
	cfg := fixtureConfig(t, "pi", piMessageStart+piDeltaHel, "")
	cfg.raw = &rawCopy{}
	f, err := os.CreateTemp("", "raw")
	if err != nil {
		t.Fatal(err)
	}
	cfg.raw.f, cfg.raw.name = f, f.Name()
	_ = f.Close() // every write now fails

	var out bytes.Buffer
	rc, err := run(cfg, &out)
	if err != nil || rc != 0 {
		t.Fatalf("run = (%d, %v), want (0, nil)", rc, err)
	}
	if !strings.Contains(out.String(), `"message_start"`) {
		t.Errorf("stdout = %q, want the filtered stream", out.String())
	}
	if p := cfg.raw.path(); p != "" {
		t.Errorf("path = %q, want none after a failed write", p)
	}
	cfg.raw.remove()
}

// exit skips mainRun's deferred cleanup, so the SIGTERM path removes the copy.
func TestRunSIGTERMRemovesTheRawCopy(t *testing.T) {
	isolatedTmp(t)
	cfg := fixtureConfig(t, "pi", piMessageStart, "exec sleep 30\n")
	cfg.raw = &rawCopy{}
	f, err := os.CreateTemp("", "raw")
	if err != nil {
		t.Fatal(err)
	}
	cfg.raw.f, cfg.raw.name = f, f.Name()

	exited := make(chan int, 1)
	orig := exit
	exit = func(code int) { exited <- code }
	t.Cleanup(func() { exit = orig })

	done := make(chan struct{})
	go func() {
		defer close(done)
		var out bytes.Buffer
		_, _ = run(cfg, &out)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, _ := os.ReadFile(cfg.logPath); strings.Contains(string(b), piMessageStart) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the Driver's message_start in the Pass log")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the SIGTERM exit")
	}
	if _, err := os.Stat(cfg.raw.name); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("raw copy after SIGTERM: stat err = %v, want it gone", err)
	}
	<-done
}

// A temp dir the copy cannot be created in must not read like a plain replay:
// the cause reaches stderr, and the live run is untouched.
func TestMainRunShadowedPiLogsAnUnwritableRawCopy(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	var paths, raws []string
	stubReplay(t, &paths, &raws)

	var stderr bytes.Buffer
	rc, _ := shadowRunDriverLog(t, "pi", piShadowRow(t), strings.TrimSuffix(piDeltaHel, "\n"), 0, &bytes.Buffer{}, &stderr)
	if rc != 0 {
		t.Fatalf("exit code = %d, want 0", rc)
	}
	if got := stderr.String(); !strings.Contains(got, "raw stdout copy") || !strings.Contains(got, "missing") {
		t.Errorf("stderr = %q, want the raw-copy CreateTemp error", stderr.String())
	}
}
