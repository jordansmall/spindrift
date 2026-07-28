package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStaleRevTracker_PriorEmptyWhenNoStateFile(t *testing.T) {
	pwd := t.TempDir()
	tracker := newStaleRevTracker(pwd)

	if got := tracker.prior(); got != "" {
		t.Fatalf("prior() = %q, want empty string", got)
	}
}

func TestStaleRevTracker_RecordThenPriorRoundTrips(t *testing.T) {
	pwd := t.TempDir()
	tracker := newStaleRevTracker(pwd)

	if err := tracker.record("deadbeef"); err != nil {
		t.Fatalf("record() error = %v", err)
	}
	if got := tracker.prior(); got != "deadbeef" {
		t.Fatalf("prior() = %q, want %q", got, "deadbeef")
	}
}

func TestStaleRevTracker_ClearThenPriorEmptyAgain(t *testing.T) {
	pwd := t.TempDir()
	tracker := newStaleRevTracker(pwd)

	if err := tracker.record("deadbeef"); err != nil {
		t.Fatalf("record() error = %v", err)
	}
	if err := tracker.clear(); err != nil {
		t.Fatalf("clear() error = %v", err)
	}
	if got := tracker.prior(); got != "" {
		t.Fatalf("prior() = %q, want empty string after clear", got)
	}
}

func TestStaleRevTracker_RecordCreatesMissingLogsDir(t *testing.T) {
	pwd := t.TempDir()
	tracker := newStaleRevTracker(pwd)

	logsDir := filepath.Join(pwd, "logs")
	if _, err := os.Stat(logsDir); !os.IsNotExist(err) {
		t.Fatalf("logs dir already exists before record(): %v", err)
	}

	if err := tracker.record("cafef00d"); err != nil {
		t.Fatalf("record() error = %v", err)
	}
	if fi, err := os.Stat(logsDir); err != nil || !fi.IsDir() {
		t.Fatalf("logs dir not created by record(): stat err = %v", err)
	}
	if got := tracker.prior(); got != "cafef00d" {
		t.Fatalf("prior() = %q, want %q", got, "cafef00d")
	}
}

func TestStaleRevTracker_ClearOnAbsentFileIsNil(t *testing.T) {
	pwd := t.TempDir()
	tracker := newStaleRevTracker(pwd)

	if err := tracker.clear(); err != nil {
		t.Fatalf("clear() error = %v, want nil for absent file", err)
	}
}
