package seamtest

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func runFj(t *testing.T, cfg FjConfig, stdin string, args ...string) (code int, stderr string) {
	t.Helper()
	for k, v := range WriteFakeConfig(t, "fj", cfg) {
		t.Setenv(k, v)
	}
	var errb bytes.Buffer
	code = fjFake(args, strings.NewReader(stdin), &errb)
	return code, errb.String()
}

func TestFjRecordsArgvAndStdin(t *testing.T) {
	dir := t.TempDir()
	cfg := FjConfig{Record: filepath.Join(dir, "argv"), StdinRecord: filepath.Join(dir, "stdin")}
	if code, _ := runFj(t, cfg, "s3cret", "auth", "add-key", "bot"); code != 0 {
		t.Fatalf("exit %d; want 0", code)
	}
	if got, want := ReadRecord(t, cfg.Record), [][]string{{"auth", "add-key", "bot"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv record = %v; want %v", got, want)
	}
	if b, _ := os.ReadFile(cfg.StdinRecord); string(b) != "s3cret" {
		t.Fatalf("stdin record = %q; want s3cret", b)
	}
}

func TestFjConfiguredExit(t *testing.T) {
	cfg := FjConfig{Record: filepath.Join(t.TempDir(), "argv"), Exit: 3}
	if code, _ := runFj(t, cfg, "", "x"); code != 3 {
		t.Fatalf("exit %d; want 3", code)
	}
}

func TestFjMissingConfig(t *testing.T) {
	t.Setenv(configEnv("fj"), "")
	if code, _ := runFj2(t); code != fakeConfigExit {
		t.Fatalf("exit %d; want %d", code, fakeConfigExit)
	}
}

func runFj2(t *testing.T) (int, string) {
	var errb bytes.Buffer
	return fjFake([]string{"x"}, strings.NewReader(""), &errb), errb.String()
}
