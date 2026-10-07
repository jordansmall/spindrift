package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostGuard(t *testing.T) {
	tests := []struct {
		name string
		host string
		want int
	}{
		{"localhost with port", "localhost:8099", http.StatusOK},
		{"localhost upper case", "LOCALHOST:8099", http.StatusOK},
		{"localhost bare", "localhost", http.StatusOK},
		{"IPv4 loopback", "127.0.0.1:8099", http.StatusOK},
		{"IPv6 loopback", "[::1]:8099", http.StatusOK},
		{"IPv6 bare", "[::1]", http.StatusOK},
		{"other IP literal", "10.0.0.5:8099", http.StatusOK},
		{"allow-listed name", "box.lan:8099", http.StatusOK},
		{"allow-listed name upper case", "BOX.LAN:8099", http.StatusOK},
		{"localhost trailing dot", "localhost.", http.StatusOK},
		{"localhost trailing dot with port", "localhost.:8099", http.StatusOK},
		{"allow-listed name trailing dot with port", "box.lan.:8099", http.StatusOK},
		{"lone dot", ".", http.StatusForbidden},
		{"foreign name", "attacker.example", http.StatusForbidden},
		{"foreign name with port", "attacker.example:8099", http.StatusForbidden},
		{"localhost as a suffix label", "localhost.attacker.example", http.StatusForbidden},
		{"empty", "", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			h := hostGuard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }), []string{"box.lan"})
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Host = tt.host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.host != "" && tt.want == http.StatusForbidden && strings.Contains(rec.Body.String(), tt.host) {
				t.Errorf("403 body %q echoes the Host header", rec.Body.String())
			}
			if called != (tt.want == http.StatusOK) {
				t.Errorf("wrapped handler called = %v, want %v", called, tt.want == http.StatusOK)
			}
		})
	}
}

func TestHostGuardBlocksBeforeRoutes(t *testing.T) {
	dir := t.TempDir()
	h := hostGuard(newServer(dir, filepath.Join(dir, statusFileName)), nil)
	for _, path := range []string{"/log", "/events"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Host = "attacker.example"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", rec.Code)
			}
		})
	}
}
