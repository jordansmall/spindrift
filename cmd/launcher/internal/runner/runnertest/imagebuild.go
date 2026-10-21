package runnertest

import (
	"bufio"
	"os"
	"strconv"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/report"
)

// InstallImageBuildPipe installs a reporter on a fresh pipe and returns a
// drain that returns the image_build states written since the last call. It
// reads with a deadline, so it can be called mid-run (from a faked execCommand)
// to observe what has been sent so far.
func InstallImageBuildPipe(t *testing.T) func() []string {
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

// WantImageBuildStates fails t unless got is exactly the want sequence.
func WantImageBuildStates(t *testing.T, got []string, want ...report.ImageBuildState) {
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
