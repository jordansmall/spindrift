package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/registrymanifest"
)

func TestRegistryProbeCache_RoundTripUnix(t *testing.T) {
	dir := t.TempDir()
	key := registryProbeCacheKey{runtime: "podman", image: "spindrift:test", networkMode: "open"}

	if err := storeRegistryProbeCache(dir, key, registrymanifest.NewUnixEndpoint(""), false); err != nil {
		t.Fatalf("storeRegistryProbeCache: %v", err)
	}

	endpoint, tcpAddHost, ok := loadRegistryProbeCache(dir, key)
	if !ok {
		t.Fatalf("loadRegistryProbeCache: got miss, want hit")
	}
	if !endpoint.IsUnix() {
		t.Errorf("endpoint = %+v, want a unix endpoint", endpoint)
	}
	if tcpAddHost {
		t.Errorf("tcpAddHost = true, want false")
	}
}

func TestRegistryProbeCache_RoundTripTCP(t *testing.T) {
	dir := t.TempDir()
	key := registryProbeCacheKey{runtime: "docker", image: "spindrift:test", networkMode: "no-host-loopback"}

	if err := storeRegistryProbeCache(dir, key, registrymanifest.NewTCPEndpoint("192.0.2.1", ""), true); err != nil {
		t.Fatalf("storeRegistryProbeCache: %v", err)
	}

	endpoint, tcpAddHost, ok := loadRegistryProbeCache(dir, key)
	if !ok {
		t.Fatalf("loadRegistryProbeCache: got miss, want hit")
	}
	if !endpoint.IsTCP() {
		t.Errorf("endpoint = %+v, want a tcp endpoint", endpoint)
	}
	if endpoint.Host() != "192.0.2.1" {
		t.Errorf("Host() = %q, want %q", endpoint.Host(), "192.0.2.1")
	}
	if !tcpAddHost {
		t.Errorf("tcpAddHost = false, want true")
	}
}

func TestRegistryProbeCache_MissNoFile(t *testing.T) {
	dir := t.TempDir()
	key := registryProbeCacheKey{runtime: "podman", image: "spindrift:test", networkMode: "open"}

	if _, _, ok := loadRegistryProbeCache(dir, key); ok {
		t.Errorf("loadRegistryProbeCache: got hit, want miss")
	}
}

// An empty pwd must write nothing anywhere, so the test also checks that no
// .spindrift directory appeared relative to its own working directory.
func TestRegistryProbeCache_EmptyPwdDisablesCache(t *testing.T) {
	key := registryProbeCacheKey{runtime: "podman", image: "spindrift:test", networkMode: "open"}

	if _, _, ok := loadRegistryProbeCache("", key); ok {
		t.Errorf("loadRegistryProbeCache(\"\", ...): got hit, want miss")
	}

	if err := storeRegistryProbeCache("", key, registrymanifest.NewUnixEndpoint(""), false); err != nil {
		t.Errorf("storeRegistryProbeCache(\"\", ...): %v, want nil", err)
	}
	if _, err := os.Stat(".spindrift"); err == nil {
		t.Errorf(".spindrift materialized despite pwd == \"\"")
		_ = os.RemoveAll(".spindrift")
	}
}

func TestRegistryProbeCache_MissMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	key := registryProbeCacheKey{runtime: "podman", image: "spindrift:test", networkMode: "open"}

	writeRegistryProbeCacheFile(t, dir, []byte("not json"))

	if _, _, ok := loadRegistryProbeCache(dir, key); ok {
		t.Errorf("loadRegistryProbeCache: got hit, want miss")
	}
}

// A version mismatch must invalidate the entry rather than misread it, so a
// future shape change cannot be loaded as the current shape.
func TestRegistryProbeCache_MissWrongVersion(t *testing.T) {
	dir := t.TempDir()
	key := registryProbeCacheKey{runtime: "podman", image: "spindrift:test", networkMode: "open"}

	entry := registryProbeCacheEntry{
		Version:     registryProbeCacheVersion + 1,
		Runtime:     key.runtime,
		Image:       key.image,
		NetworkMode: key.networkMode,
		Transport:   "unix",
	}
	writeRegistryProbeCacheEntry(t, dir, entry)

	if _, _, ok := loadRegistryProbeCache(dir, key); ok {
		t.Errorf("loadRegistryProbeCache: got hit, want miss")
	}
}

func TestRegistryProbeCache_MissUnrecognisedTransport(t *testing.T) {
	dir := t.TempDir()
	key := registryProbeCacheKey{runtime: "podman", image: "spindrift:test", networkMode: "open"}

	entry := registryProbeCacheEntry{
		Version:     registryProbeCacheVersion,
		Runtime:     key.runtime,
		Image:       key.image,
		NetworkMode: key.networkMode,
		Transport:   "carrier-pigeon",
	}
	writeRegistryProbeCacheEntry(t, dir, entry)

	if _, _, ok := loadRegistryProbeCache(dir, key); ok {
		t.Errorf("loadRegistryProbeCache: got hit, want miss")
	}
}

// Each case varies one key axis alone, so a matcher that ignores that axis
// fails on its own case rather than hiding behind the others.
func TestRegistryProbeCache_MissKeyAxisDiffers(t *testing.T) {
	base := registryProbeCacheKey{runtime: "podman", image: "spindrift:test", networkMode: "open"}

	cases := []struct {
		name string
		want registryProbeCacheKey
	}{
		{"runtime differs", registryProbeCacheKey{runtime: "docker", image: base.image, networkMode: base.networkMode}},
		{"image differs", registryProbeCacheKey{runtime: base.runtime, image: "spindrift:other", networkMode: base.networkMode}},
		{"networkMode differs", registryProbeCacheKey{runtime: base.runtime, image: base.image, networkMode: "none"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := storeRegistryProbeCache(dir, base, registrymanifest.NewUnixEndpoint(""), false); err != nil {
				t.Fatalf("storeRegistryProbeCache: %v", err)
			}
			if _, _, ok := loadRegistryProbeCache(dir, tc.want); ok {
				t.Errorf("loadRegistryProbeCache: got hit, want miss")
			}
		})
	}
}

// A tcp entry with no host is an unusable decision, so it must miss rather
// than count as a stored verdict.
func TestRegistryProbeCache_MissTCPEmptyHost(t *testing.T) {
	dir := t.TempDir()
	key := registryProbeCacheKey{runtime: "podman", image: "spindrift:test", networkMode: "open"}

	entry := registryProbeCacheEntry{
		Version:     registryProbeCacheVersion,
		Runtime:     key.runtime,
		Image:       key.image,
		NetworkMode: key.networkMode,
		Transport:   "tcp",
		TCPHost:     "",
	}
	writeRegistryProbeCacheEntry(t, dir, entry)

	if _, _, ok := loadRegistryProbeCache(dir, key); ok {
		t.Errorf("loadRegistryProbeCache: got hit, want miss")
	}
}

// A zero-value endpoint is neither unix nor tcp, and such an incoherent
// verdict must never reach disk, so the test also checks for no file.
func TestRegistryProbeCache_StoreZeroValueEndpointErrors(t *testing.T) {
	dir := t.TempDir()
	key := registryProbeCacheKey{runtime: "podman", image: "spindrift:test", networkMode: "open"}

	if err := storeRegistryProbeCache(dir, key, registrymanifest.Endpoint{}, false); err == nil {
		t.Fatalf("storeRegistryProbeCache: got nil error, want non-nil")
	}
	if _, err := os.Stat(registryProbeCachePath(dir)); err == nil {
		t.Errorf("cache file exists despite the rejected store")
	}
}

func TestRegistryProbeCache_StoreOverwritesStaleEntry(t *testing.T) {
	dir := t.TempDir()
	staleKey := registryProbeCacheKey{runtime: "podman", image: "spindrift:old", networkMode: "open"}
	freshKey := registryProbeCacheKey{runtime: "podman", image: "spindrift:new", networkMode: "open"}

	if err := storeRegistryProbeCache(dir, staleKey, registrymanifest.NewUnixEndpoint(""), false); err != nil {
		t.Fatalf("storeRegistryProbeCache(stale): %v", err)
	}

	// The cache holds one entry at a time, so freshKey misses against staleKey's.
	if _, _, ok := loadRegistryProbeCache(dir, freshKey); ok {
		t.Fatalf("loadRegistryProbeCache(freshKey): got hit before the fresh store, want miss")
	}

	if err := storeRegistryProbeCache(dir, freshKey, registrymanifest.NewTCPEndpoint("198.51.100.7", ""), true); err != nil {
		t.Fatalf("storeRegistryProbeCache(fresh): %v", err)
	}

	if _, _, ok := loadRegistryProbeCache(dir, staleKey); ok {
		t.Errorf("loadRegistryProbeCache(staleKey): got hit after overwrite, want miss")
	}
	endpoint, tcpAddHost, ok := loadRegistryProbeCache(dir, freshKey)
	if !ok {
		t.Fatalf("loadRegistryProbeCache(freshKey): got miss after overwrite, want hit")
	}
	if !endpoint.IsTCP() || endpoint.Host() != "198.51.100.7" {
		t.Errorf("endpoint = %+v, want tcp host 198.51.100.7", endpoint)
	}
	if !tcpAddHost {
		t.Errorf("tcpAddHost = false, want true")
	}
}

// The store writes a temp file and renames it into place, so the directory
// listing must hold the final cache file alone.
func TestRegistryProbeCache_StoreOverwriteLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	first := registryProbeCacheKey{runtime: "podman", image: "spindrift:old", networkMode: "open"}
	second := registryProbeCacheKey{runtime: "podman", image: "spindrift:new", networkMode: "open"}

	if err := storeRegistryProbeCache(dir, first, registrymanifest.NewUnixEndpoint(""), false); err != nil {
		t.Fatalf("storeRegistryProbeCache(first): %v", err)
	}
	if err := storeRegistryProbeCache(dir, second, registrymanifest.NewTCPEndpoint("198.51.100.7", ""), true); err != nil {
		t.Fatalf("storeRegistryProbeCache(second): %v", err)
	}

	entries, err := os.ReadDir(filepath.Dir(registryProbeCachePath(dir)))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "registry-probe-cache.json" {
		t.Errorf("cache dir entries = %v, want exactly [registry-probe-cache.json]", names)
	}
}

// Creating a directory at the cache file's path is how this test forces the
// final rename to fail. The store must then clean up its own temp file.
func TestRegistryProbeCache_StoreRenameFailureLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := registryProbeCachePath(dir)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(path as a directory): %v", err)
	}

	key := registryProbeCacheKey{runtime: "podman", image: "spindrift:test", networkMode: "open"}
	if err := storeRegistryProbeCache(dir, key, registrymanifest.NewUnixEndpoint(""), false); err == nil {
		t.Fatal("storeRegistryProbeCache: got nil error, want non-nil (rename onto a directory must fail)")
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "registry-probe-cache.json" || !entries[0].IsDir() {
		t.Errorf("cache dir entries = %v, want only the pre-existing directory, no leftover temp file", entries)
	}
}

// A plain os.WriteFile truncates at open and writes the bytes as a separate
// step, so a concurrent reader can see an empty, unparseable file. The store
// writes a temp file and renames it instead, and this test pins that: the
// reader goroutine fails if it ever parses a torn file.
func TestRegistryProbeCache_StoreIsAtomicUnderConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	path := registryProbeCachePath(dir)

	seed := registryProbeCacheKey{runtime: "podman", image: "spindrift:seed", networkMode: "open"}
	if err := storeRegistryProbeCache(dir, seed, registrymanifest.NewUnixEndpoint(""), false); err != nil {
		t.Fatalf("storeRegistryProbeCache(seed): %v", err)
	}

	stop := make(chan struct{})
	tornCh := make(chan string, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			b, err := os.ReadFile(path)
			if err != nil {
				continue // a read racing a rename is a plain miss, not torn.
			}
			var entry registryProbeCacheEntry
			if err := json.Unmarshal(b, &entry); err != nil {
				select {
				case tornCh <- fmt.Sprintf("read a torn cache file: %v (bytes: %q)", err, b):
				default:
				}
				return
			}
		}
	}()

	deadline := time.Now().Add(300 * time.Millisecond)
	for i := 0; time.Now().Before(deadline); i++ {
		k := registryProbeCacheKey{
			runtime:     "podman",
			image:       fmt.Sprintf("spindrift:v%d-%s", i, strings.Repeat("x", i%40)),
			networkMode: "open",
		}
		if err := storeRegistryProbeCache(dir, k, registrymanifest.NewUnixEndpoint(""), i%2 == 0); err != nil {
			t.Fatalf("storeRegistryProbeCache: %v", err)
		}
	}
	close(stop)
	wg.Wait()

	select {
	case msg := <-tornCh:
		t.Fatal(msg)
	default:
	}
}

// writeRegistryProbeCacheFile bypasses storeRegistryProbeCache to plant
// malformed or hand-built payloads a well-formed store could never produce.
func writeRegistryProbeCacheFile(t *testing.T, dir string, b []byte) {
	t.Helper()
	path := registryProbeCachePath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func writeRegistryProbeCacheEntry(t *testing.T, dir string, entry registryProbeCacheEntry) {
	t.Helper()
	b, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	writeRegistryProbeCacheFile(t, dir, b)
}
