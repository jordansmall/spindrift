package main

import (
	"bytes"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitInit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init: %v: %s", err, out)
	}
	return dir
}

func TestStatusPathResolvesGitDir(t *testing.T) {
	checkout := gitInit(t)
	sub := filepath.Join(checkout, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := statusPathFor(sub)
	if err != nil {
		t.Fatal(err)
	}
	wantDir, _ := filepath.EvalSymlinks(filepath.Join(checkout, ".git"))
	gotDir, _ := filepath.EvalSymlinks(filepath.Dir(got))
	if gotDir != wantDir || filepath.Base(got) != statusFileName {
		t.Errorf("statusPathFor = %s, want it in %s", got, wantDir)
	}
}

func TestStatusPathNotAGitCheckout(t *testing.T) {
	if _, err := statusPathFor(t.TempDir()); err == nil {
		t.Error("want error for non-git dir")
	}
}

func TestRunTakenPortExitsNonZero(t *testing.T) {
	checkout := gitInit(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	var stderr bytes.Buffer
	code := run([]string{"--checkout", checkout, "--listen", addr}, &stderr, nil)
	if code == 0 {
		t.Fatal("exit 0 on taken port")
	}
	if !strings.Contains(stderr.String(), "dashboard: listen "+addr) {
		t.Errorf("stderr does not name the address: %q", stderr.String())
	}
}

func TestRunAnnouncesBoundAddressOnce(t *testing.T) {
	checkout := gitInit(t)
	var stderr bytes.Buffer
	ready := make(chan net.Addr, 1)
	stop := make(chan struct{})
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"--checkout", checkout, "--listen", "127.0.0.1:0"}, &stderr, &runHooks{ready: ready, stop: stop})
	}()
	addr := (<-ready).String()
	close(stop)
	if code := <-done; code != 0 {
		t.Fatalf("exit = %d, stderr %q", code, stderr.String())
	}
	out := stderr.String()
	if strings.Count(out, "\n") != 1 {
		t.Errorf("want exactly one stderr line, got %q", out)
	}
	if !strings.Contains(out, "http://"+addr) || !strings.Contains(out, "unauthenticated") {
		t.Errorf("announcement = %q", out)
	}
}

func TestRunBadCheckout(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"--checkout", t.TempDir()}, &stderr, nil); code == 0 {
		t.Error("exit 0 for non-git checkout")
	}
	if !strings.HasPrefix(stderr.String(), "dashboard: ") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestResolveCheckoutSubdirectoryIsToplevel(t *testing.T) {
	checkout := gitInit(t)
	sub := filepath.Join(checkout, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveCheckout(sub)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(checkout)
	if gotReal, _ := filepath.EvalSymlinks(got); gotReal != want {
		t.Errorf("resolveCheckout(%s) = %s, want %s", sub, got, want)
	}
}

func TestResolveCheckoutNotAGitCheckoutNamesFlag(t *testing.T) {
	dir := t.TempDir()
	if _, err := resolveCheckout(dir); err == nil || !strings.Contains(err.Error(), "--checkout "+dir) {
		t.Errorf("err = %v, want it to name --checkout %s", err, dir)
	}
}

func TestRunEmptyAllowHostExitsTwo(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"--allow-host", ""}, &stderr, nil); code != 2 {
		t.Errorf("exit = %d, want 2; stderr %q", code, stderr.String())
	}
}

func TestRunRejectsForeignHostHeader(t *testing.T) {
	checkout := gitInit(t)
	var stderr bytes.Buffer
	ready := make(chan net.Addr, 1)
	stop := make(chan struct{})
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"--checkout", checkout, "--listen", "127.0.0.1:0"}, &stderr, &runHooks{ready: ready, stop: stop})
	}()
	addr := (<-ready).String()
	defer func() { close(stop); <-done }()

	for _, tt := range []struct {
		host string
		want int
	}{
		{"attacker.example", http.StatusForbidden},
		{addr, http.StatusOK},
	} {
		req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = tt.host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tt.want {
			t.Errorf("Host %q: status = %d, want %d", tt.host, resp.StatusCode, tt.want)
		}
	}
}

func TestRunMalformedAllowHostExitsTwo(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{"box.lan:8099", "without a port"},
		{"[box.lan]", "without a port"},
		{"[::1]:8099", "without a port"},
		{"::1", "IP literals are always allowed"},
		{"[::1]", "IP literals are always allowed"},
		{"10.0.0.5", "IP literals are always allowed"},
		{".", "must not be empty"},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := run([]string{"--allow-host", tt.value}, &stderr, nil); code != 2 {
				t.Errorf("exit = %d, want 2; stderr %q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.want) {
				t.Errorf("stderr %q, want it to contain %q", stderr.String(), tt.want)
			}
		})
	}
}
