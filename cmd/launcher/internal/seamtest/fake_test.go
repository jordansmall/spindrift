package seamtest

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDispatch(t *testing.T) {
	saved := fakes
	t.Cleanup(func() { fakes = saved })
	fakes = map[string]func([]string) int{
		"tool": func(args []string) int { return 40 + len(args) },
	}
	ran := false
	runTests := func() int { ran = true; return 7 }

	if got := dispatch("tool", []string{"a", "b"}, runTests); got != 42 || ran {
		t.Fatalf("registered name: got %d, ran=%v; want 42, false", got, ran)
	}
	if got := dispatch("seamtest.test", nil, runTests); got != 7 || !ran {
		t.Fatalf("unregistered name: got %d, ran=%v; want 7, true", got, ran)
	}
}

func TestLoadConfigRoundTrip(t *testing.T) {
	type cfg struct {
		Record string
		N      int
	}
	want := cfg{Record: "/x/y", N: 3}
	env := WriteFakeConfig(t, "podman", want)
	for k, v := range env {
		t.Setenv(k, v)
	}
	var got cfg
	if err := loadConfig("podman", &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %+v; want %+v", got, want)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	var c struct{}
	t.Setenv(configEnv("podman"), "")
	if err := loadConfig("podman", &c); err == nil {
		t.Error("unset env: want error")
	}
	t.Setenv(configEnv("podman"), filepath.Join(t.TempDir(), "missing.json"))
	if err := loadConfig("podman", &c); err == nil {
		t.Error("missing file: want error")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(configEnv("podman"), bad)
	if err := loadConfig("podman", &c); err == nil {
		t.Error("malformed file: want error")
	}
}

func TestAppendRecordRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rec")
	if got := ReadRecord(t, path); len(got) != 0 {
		t.Fatalf("missing file: got %v; want none", got)
	}
	first := []string{"run", "--name", "a b", "multi\nline"}
	second := []string{"rm", "-f", "x"}
	prior, err := appendRecord(path, first)
	if err != nil || len(prior) != 0 {
		t.Fatalf("first append: prior=%v err=%v", prior, err)
	}
	prior, err = appendRecord(path, second)
	if err != nil || !reflect.DeepEqual(prior, [][]string{first}) {
		t.Fatalf("second append: prior=%v err=%v", prior, err)
	}
	if got := ReadRecord(t, path); !reflect.DeepEqual(got, [][]string{first, second}) {
		t.Fatalf("ReadRecord = %v", got)
	}
}

func TestInstallFakes(t *testing.T) {
	dir := InstallFakes(t, "podman")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.Readlink(filepath.Join(dir, "podman"))
	if err != nil || got != exe {
		t.Fatalf("symlink = %q, %v; want %q", got, err, exe)
	}
}
