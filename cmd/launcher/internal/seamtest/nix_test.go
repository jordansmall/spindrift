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

func TestNixRunExecsConfiguredProgramWithItsFlagsBeforeTheVerb(t *testing.T) {
	cfg := NixConfig{Exec: "/bin/launcher", ExecArgs: []string{"--input", "doc.json"}}
	prog, argv, ok := nixRunExec(cfg, []string{"run", "git+file:///repo?rev=abc#", "--", "dispatch", "--max-jobs", "1"})
	if !ok || prog != "/bin/launcher" {
		t.Fatalf("nixRunExec = %q, %v; want /bin/launcher, true", prog, ok)
	}
	want := []string{"/bin/launcher", "--input", "doc.json", "dispatch", "--max-jobs", "1"}
	if !reflect.DeepEqual(argv, want) {
		t.Errorf("argv = %v; want %v", argv, want)
	}
}

func TestNixRunDoesNotExecWithoutConfigOrForOtherVerbs(t *testing.T) {
	for _, tc := range []struct {
		cfg  NixConfig
		args []string
	}{
		{NixConfig{}, []string{"run", ".#", "--", "dispatch"}},
		{NixConfig{Exec: "/bin/launcher"}, []string{"develop", ".#ci"}},
		{NixConfig{Exec: "/bin/launcher"}, []string{"run", ".#"}},
	} {
		if _, _, ok := nixRunExec(tc.cfg, tc.args); ok {
			t.Errorf("nixRunExec(%v, %v) ok; want not", tc.cfg, tc.args)
		}
	}
}

func TestNixRunDoctorExitsCleanly(t *testing.T) {
	cfg := NixConfig{Record: filepath.Join(t.TempDir(), "rec"), Exec: "/bin/launcher"}
	if got := runNix(t, cfg, "run", ".#", "--", "doctor"); got != 0 {
		t.Errorf("exit %d; want 0", got)
	}
}

func TestNixRunStubsDoctorUnlessExecDoctor(t *testing.T) {
	args := []string{"run", ".#", "--", "doctor", "--butler"}
	if !nixRunStubsDoctor(NixConfig{Exec: "/bin/launcher"}, args) {
		t.Error("doctor not stubbed by default")
	}
	if nixRunStubsDoctor(NixConfig{Exec: "/bin/launcher", ExecDoctor: true}, args) {
		t.Error("doctor stubbed despite ExecDoctor")
	}
	if nixRunStubsDoctor(NixConfig{Exec: "/bin/launcher"}, []string{"run", ".#", "--", "dispatch"}) {
		t.Error("dispatch stubbed")
	}
}
