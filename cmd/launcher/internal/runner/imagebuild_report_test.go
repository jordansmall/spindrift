package runner

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/report"
)

// installImageBuildPipe installs a reporter on a fresh pipe and returns a
// drain that returns the image_build states written since the last call. It
// reads with a deadline, so it can be called mid-run (from a faked execCommand)
// to observe what has been sent so far.
func installImageBuildPipe(t *testing.T) func() []string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	t.Cleanup(func() { w.Close() })
	fd := int(w.Fd())
	rep := report.FromEnv(func(k string) string {
		if k == "SPINDRIFT_REPORT_FD" {
			return strconv.Itoa(fd)
		}
		return ""
	}, os.Stderr)
	if rep == nil {
		t.Fatal("report.FromEnv returned nil")
	}
	t.Cleanup(report.Install(rep))

	br := bufio.NewReader(r)
	return func() []string {
		var states []string
		for {
			_ = r.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			line, err := br.ReadBytes('\n')
			if err != nil {
				return states
			}
			var rec report.Record
			if err := rec.UnmarshalJSON(line); err != nil {
				t.Fatalf("unmarshal %q: %v", line, err)
			}
			if rec.Event != report.EventImageBuild || !rec.Key.IsZero() {
				t.Fatalf("record = %+v, want a keyless image_build", rec)
			}
			states = append(states, rec.State)
		}
	}
}

func wantStates(t *testing.T, got []string, want ...report.ImageBuildState) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("image_build states = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != string(want[i]) {
			t.Fatalf("image_build states = %v, want %v", got, want)
		}
	}
}

func TestOCIEnsureReady_WarmReportsNothing(t *testing.T) {
	redirectImageLockDir(t)
	drain := installImageBuildPipe(t)
	cli, _ := newFakeCLI(t, fakeCall{exit: 0}) // image inspect: present
	a := &ociAdapter{cli: cli, image: "spindrift:abc123"}

	if err := a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	wantStates(t, drain())
}

func TestOCIEnsureReady_SiblingRealisedReportsNothing(t *testing.T) {
	redirectImageLockDir(t)
	drain := installImageBuildPipe(t)
	cli, _ := newFakeCLI(t, fakeCall{exit: 1}, fakeCall{exit: 0}) // absent, then present under the lock
	a := &ociAdapter{cli: cli, image: "spindrift:abc123"}

	if err := a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	wantStates(t, drain())
}

func TestOCIEnsureReady_ColdBuildReportsStartedBeforeNixThenFinished(t *testing.T) {
	redirectImageLockDir(t)
	drain := installImageBuildPipe(t)
	cli, _ := newFakeCLI(t, fakeCall{exit: 1}, fakeCall{exit: 1}, fakeCall{}, fakeCall{})
	nixScript, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var beforeNix []string
	execCommand = func(name string, args ...string) *exec.Cmd {
		beforeNix = drain()
		return exec.Command(nixScript, args...)
	}
	a := &ociAdapter{
		cli:          cli,
		image:        "spindrift:abc123",
		imageDrv:     "/nix/store/fake.drv",
		imageArchive: "/tmp/spindrift-image.tar",
		imageTag:     "spindrift:abc123",
	}

	if err := a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	wantStates(t, beforeNix, report.ImageBuildStarted)
	wantStates(t, drain(), report.ImageBuildFinished)
}

func TestOCIEnsureReady_ColdBuildFailureStillReportsFinished(t *testing.T) {
	redirectImageLockDir(t)
	drain := installImageBuildPipe(t)
	cli, _ := newFakeCLI(t, fakeCall{exit: 1}, fakeCall{exit: 1})
	stubHostNix(t, fakeCall{exit: 1, stdout: "genuine derivation error"})
	a := &ociAdapter{cli: cli, image: "spindrift:abc123", imageDrv: "/nix/store/fake.drv"}

	if err := a.EnsureReady(); err == nil {
		t.Fatal("EnsureReady = nil, want the nix build failure")
	}
	wantStates(t, drain(), report.ImageBuildStarted, report.ImageBuildFinished)
}

func TestBwrapBuildEnsureReady_ReportsStartedThenFinished(t *testing.T) {
	drain := installImageBuildPipe(t)
	stubHostNix(t, fakeCall{exit: 0})
	a := &bwrapBuildAdapter{agentFilesDrv: "/fake/files.drv"}

	if err := a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	wantStates(t, drain(), report.ImageBuildStarted, report.ImageBuildFinished)
}

func TestBwrapBuildEnsureReady_RealizeFailureStillReportsFinished(t *testing.T) {
	drain := installImageBuildPipe(t)
	stubHostNix(t, fakeCall{exit: 1})
	a := &bwrapBuildAdapter{agentFilesDrv: "/fake/files.drv"}

	if err := a.EnsureReady(); err == nil {
		t.Fatal("EnsureReady = nil, want the realize failure")
	}
	wantStates(t, drain(), report.ImageBuildStarted, report.ImageBuildFinished)
}

func TestImageBuild_NoReporterInstalledIsHarmless(t *testing.T) {
	redirectImageLockDir(t)
	cli, _ := newFakeCLI(t, fakeCall{exit: 0})
	a := &ociAdapter{cli: cli, image: "spindrift:abc123"}
	if err := a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
}
