package seamtest

import (
	"bytes"
	"path/filepath"
	"reflect"
	"testing"
)

func runNix(t *testing.T, cfg NixConfig, args ...string) int {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv(configEnv("nix"), "")
	WriteFakeConfigFile(t, dir, "nix", cfg)
	return nixFake(args, &bytes.Buffer{})
}

func TestNixRegistered(t *testing.T) {
	if _, ok := fakes["nix"]; !ok {
		t.Fatal("no nix fake registered")
	}
}

func TestNixDevelopSucceedsOnlyForConfiguredShells(t *testing.T) {
	cfg := NixConfig{Record: filepath.Join(t.TempDir(), "rec"), DevShells: []string{"ci"}}
	for _, tc := range []struct {
		args []string
		want int
	}{
		{[]string{"develop", ".#ci", "--command", "true"}, 0},
		{[]string{"develop", ".#default", "--command", "true"}, 1},
		{[]string{"build", ".#ci"}, 1},
	} {
		if got := runNix(t, cfg, tc.args...); got != tc.want {
			t.Errorf("nix %v: exit %d; want %d", tc.args, got, tc.want)
		}
	}
}

func TestNixRecordsArgv(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "rec")
	runNix(t, NixConfig{Record: rec}, "develop", ".#x", "--command", "true")
	want := [][]string{{"develop", ".#x", "--command", "true"}}
	if got := ReadRecord(t, rec); !reflect.DeepEqual(got, want) {
		t.Errorf("record = %v; want %v", got, want)
	}
}

func TestNixMissingConfigExits(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv(configEnv("nix"), "")
	if code := nixFake([]string{"develop", ".#ci"}, &bytes.Buffer{}); code != fakeConfigExit {
		t.Errorf("exit %d; want %d", code, fakeConfigExit)
	}
}
