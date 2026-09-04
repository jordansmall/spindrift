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

// TestRegistryProbeCache_RoundTripUnix verifies that a stored unix verdict
// loads back as a unix endpoint with tcpAddHost false, unchanged.
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

// TestRegistryProbeCache_RoundTripTCP verifies that a stored tcp verdict
// loads back with the same host and tcpAddHost true.
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

// TestRegistryProbeCache_MissNoFile verifies that an empty pwd directory
// with no cache file yet is a plain miss.
func TestRegistryProbeCache_MissNoFile(t *testing.T) {
	dir := t.TempDir()
	key := registryProbeCacheKey{runtime: "podman", image: "spindrift:test", networkMode: "open"}

	if _, _, ok := loadRegistryProbeCache(dir, key); ok {
		t.Errorf("loadRegistryProbeCache: got hit, want miss")
	}
}

// TestRegistryProbeCache_EmptyPwdDisablesCache verifies that pwd == ""
// disables the cache entirely: a load is always a miss, and a store writes
// nothing anywhere (in particular, no ".spindrift" directory materializes
// relative to the test's own working directory).
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

// TestRegistryProbeCache_MissMalformedJSON verifies that garbage bytes on
// disk are a miss, not an error.
func TestRegistryProbeCache_MissMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	key := registryProbeCacheKey{runtime: "podman", image: "spindrift:test", networkMode: "open"}

	writeRegistryProbeCacheFile(t, dir, []byte("not json"))

	if _, _, ok := loadRegistryProbeCache(dir, key); ok {
		t.Errorf("loadRegistryProbeCache: got hit, want miss")
	}
}

// TestRegistryProbeCache_MissWrongVersion verifies that a stored entry
// carrying a version other than the current one is a miss, so a future
// shape change invalidates rather than misreads.
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

// TestRegistryProbeCache_MissUnrecognisedTransport verifies that a stored
// transport string other than "unix"/"tcp" is a miss.
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

// TestRegistryProbeCache_MissKeyAxisDiffers verifies that a stored entry
// misses when the live key differs on any single axis -- runtime, image, or
// networkMode -- independent of the others.
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

// TestRegistryProbeCache_MissTCPEmptyHost verifies that a stored tcp entry
// with no host is a miss -- an unusable decision, distinct from a
// genuinely-absent entry.
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

// TestRegistryProbeCache_StoreZeroValueEndpointErrors verifies that storing
// a zero-value endpoint (neither unix nor tcp) returns an error and leaves
// no file behind -- an incoherent verdict must never reach disk.
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

// TestRegistryProbeCache_StoreOverwritesStaleEntry verifies that storing a
// new verdict after a miss overwrites a stale on-disk entry, and the new
// entry then loads correctly.
func TestRegistryProbeCache_StoreOverwritesStaleEntry(t *testing.T) {
	dir := t.TempDir()
	staleKey := registryProbeCacheKey{runtime: "podman", image: "spindrift:old", networkMode: "open"}
	freshKey := registryProbeCacheKey{runtime: "podman", image: "spindrift:new", networkMode: "open"}

	if err := storeRegistryProbeCache(dir, staleKey, registrymanifest.NewUnixEndpoint(""), false); err != nil {
		t.Fatalf("storeRegistryProbeCache(stale): %v", err)
	}

	// staleKey's entry is now on disk; freshKey misses against it.
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

// TestRegistryProbeCache_StoreOverwriteLeavesNoTempFile verifies that
// storing a second verdict over an existing cache file leaves exactly the
// final cache file behind -- no stray temp file from the write-then-rename
// sequence survives in the cache dir.
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

// TestRegistryProbeCache_StoreRenameFailureLeavesNoTempFile verifies that
// when the final rename cannot complete -- here forced by a directory
// already occupying the cache file's path -- storeRegistryProbeCache returns
// an error and removes its own temp file rather than littering the cache
// dir with it.
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

// TestRegistryProbeCache_StoreIsAtomicUnderConcurrentWrites verifies that a
// reader racing a writer never observes a torn cache file -- a truncated or
// otherwise unparseable file mid-write. A plain os.WriteFile truncates the
// file at open, then writes the new bytes as a separate step, leaving a
// window where a concurrent read sees an empty (and so unparseable) file;
// writing to a temp file and renaming it into place closes that window,
// since rename is atomic and a reader always sees either the whole old file
// or the whole new one.
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

// writeRegistryProbeCacheFile writes raw bytes directly to the cache path
// under dir, bypassing storeRegistryProbeCache -- used to plant malformed or
// hand-built payloads a well-formed store could never produce.
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

// writeRegistryProbeCacheEntry JSON-encodes entry and plants it at the
// cache path under dir.
func writeRegistryProbeCacheEntry(t *testing.T, dir string, entry registryProbeCacheEntry) {
	t.Helper()
	b, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	writeRegistryProbeCacheFile(t, dir, b)
}
