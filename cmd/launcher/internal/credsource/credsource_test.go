package credsource

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestStatic(t *testing.T) {
	t.Parallel()
	if got := Static("v").Value(); got != "v" {
		t.Fatalf("Value = %q, want v", got)
	}
	if got := Static("").Value(); got != "" {
		t.Fatalf("Value = %q, want empty", got)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFileRotationAndFallback(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tok")
	src := File(path, Static("startup"))
	if got := src.Value(); got != "startup" {
		t.Fatalf("before the file exists = %q, want the fallback", got)
	}
	write(t, path, "  first\n")
	if got := src.Value(); got != "first" {
		t.Fatalf("after first write = %q, want trimmed first", got)
	}
	write(t, path, "second\n")
	if got := src.Value(); got != "second" {
		t.Fatalf("after rotation = %q, want second", got)
	}
}

func TestFileKeepsLastGoodValue(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tok")
	src := File(path, Static("startup"))
	write(t, path, "good")
	if src.Value() != "good" {
		t.Fatal("want good")
	}
	write(t, path, "\n")
	if got := src.Value(); got != "good" {
		t.Errorf("empty file = %q, want the last good value", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := src.Value(); got != "good" {
		t.Errorf("missing file = %q, want the last good value", got)
	}
}

func TestFileNilFallback(t *testing.T) {
	t.Parallel()
	if got := File(filepath.Join(t.TempDir(), "none"), nil).Value(); got != "" {
		t.Fatalf("Value = %q, want empty", got)
	}
}

func TestFileFallbackTracksUntilFirstGoodRead(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tok")
	cmd, err := NewCommand("E", "c", func(string) (string, error) { return "one\n", nil })
	if err != nil {
		t.Fatal(err)
	}
	src := File(path, cmd)
	if src.Value() != "one" {
		t.Fatalf("Value = %q, want the command's value", src.Value())
	}
	write(t, path, "filed")
	if src.Value() != "filed" {
		t.Fatalf("Value = %q, want the file to win once readable", src.Value())
	}
}

func TestCommandResolvesAndRefreshes(t *testing.T) {
	t.Parallel()
	out := "one\n"
	c, err := NewCommand("E", "vault get e", func(cmd string) (string, error) {
		if cmd != "vault get e" {
			t.Errorf("ran %q", cmd)
		}
		return out, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.Env() != "E" || c.Value() != "one" {
		t.Fatalf("Env=%q Value=%q", c.Env(), c.Value())
	}
	out = "two\n"
	if err := c.Refresh(); err != nil || c.Value() != "two" {
		t.Fatalf("Refresh err=%v Value=%q", err, c.Value())
	}
}

func TestCommandRefreshFailureKeepsPreviousValue(t *testing.T) {
	t.Parallel()
	fail := false
	c, err := NewCommand("E", "vault get hunter2", func(string) (string, error) {
		if fail {
			return "", errors.New("down hunter2")
		}
		return "old\n", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fail = true
	err = c.Refresh()
	if err == nil {
		t.Fatal("want an error")
	}
	if c.Value() != "old" {
		t.Errorf("Value = %q, want old", c.Value())
	}
	if !strings.Contains(err.Error(), "E") || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error %q should name the env and stay value-free", err)
	}
}

func TestNewCommandFailureIsValueFree(t *testing.T) {
	t.Parallel()
	c, err := NewCommand("E", "vault get hunter2", func(string) (string, error) { return "", errors.New("x") })
	if err == nil || c != nil {
		t.Fatalf("got %v, %v; want an error and no source", c, err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error %q leaks the command", err)
	}
}

func TestConcurrentUse(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tok")
	var n atomic.Int64
	c, err := NewCommand("E", "c", func(string) (string, error) { return "v\n", nil })
	if err != nil {
		t.Fatal(err)
	}
	f := File(path, c)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				_ = f.Value()
				_ = c.Refresh()
				n.Add(1)
			}
		}()
	}
	for range 50 {
		write(t, path, "tok")
	}
	wg.Wait()
}
