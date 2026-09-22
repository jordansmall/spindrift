package report

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
)

func TestInstallDefault_ForwardsAndRestores(t *testing.T) {
	if Default() != nil {
		t.Fatalf("Default() before Install = %v, want nil", Default())
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()

	rep := &Reporter{fd: int(w.Fd())}
	restore := Install(rep)
	if Default() != rep {
		t.Fatalf("Default() after Install = %v, want %v", Default(), rep)
	}

	Box("42", "fix-pass-1")
	restore()
	if Default() != nil {
		t.Fatalf("Default() after restore = %v, want nil", Default())
	}
	w.Close()

	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		t.Fatalf("no line read: %v", scanner.Err())
	}
	var rec Record
	if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := Record{Event: "box", Issue: "42", Phase: "fix-pass-1"}
	if rec != want {
		t.Errorf("record = %+v, want %+v", rec, want)
	}

	// Package-level Settled/Box on a nil default must not panic.
	Settled("1", "merged", "")
}
