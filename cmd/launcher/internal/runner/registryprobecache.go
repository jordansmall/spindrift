package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// registryProbeCacheVersion tags the on-disk payload shape. Bumping it on a
// field change makes an old file miss cleanly rather than being misread as
// the new shape.
const registryProbeCacheVersion = 1

// registryProbeCacheKey holds the inputs the transport verdict depends on; a
// stored key differing from the live key is a miss, never a wrong answer. An
// old image missing the probe-registry-socket verb reads as socket-incapable
// (#3120), and oci.go hard-errors when a socket-incapable verdict meets a
// mode that denies host loopback, so both are in the key.
type registryProbeCacheKey struct {
	// runtime is the resolved binary, not the raw infra.runtime string, so
	// aliases that resolve to one binary share a cache entry.
	runtime     string
	image       string
	networkMode string
}

// registryProbeCacheEntry is the on-disk payload. It carries the verdict in
// its own shape rather than an embedded registrymanifest.Endpoint, whose JSON
// codec renders the ADR-0045 "unix://"/"tcp://host:port" string form and
// rejects the path-less, port-less values the probe returns (the caller mints
// the real path or port afterwards).
type registryProbeCacheEntry struct {
	Version     int    `json:"version"`
	Runtime     string `json:"runtime"`
	Image       string `json:"image"`
	NetworkMode string `json:"networkMode"`
	Transport   string `json:"transport"`
	TCPHost     string `json:"tcpHost,omitempty"`
	TCPAddHost  bool   `json:"tcpAddHost"`
}

// registryProbeCachePath returns the cache path for pwd, or "" when pwd is
// "", which disables the cache in both load and store: a launcher with no
// working directory has nowhere to write. The path is fixed rather than keyed
// into the filename so the operator's force-re-probe gesture is "delete this
// one documented file", not a glob over opaque names.
func registryProbeCachePath(pwd string) string {
	if pwd == "" {
		return ""
	}
	return filepath.Join(pwd, ".spindrift", "registry-probe-cache.json")
}

// loadRegistryProbeCache returns the remembered transport decision for want,
// or ok=false on any miss: no pwd, no file, an unreadable file, malformed
// JSON, an unrecognised version or transport, a differing key, or a tcp entry
// with no host. Every miss path falls through to a fresh probe rather than
// returning an error, so a damaged cache file can never fail a dispatch.
func loadRegistryProbeCache(pwd string, want registryProbeCacheKey) (endpoint registrymanifest.Endpoint, tcpAddHost bool, ok bool) {
	path := registryProbeCachePath(pwd)
	if path == "" {
		return registrymanifest.Endpoint{}, false, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return registrymanifest.Endpoint{}, false, false
	}
	var entry registryProbeCacheEntry
	if err := json.Unmarshal(b, &entry); err != nil {
		return registrymanifest.Endpoint{}, false, false
	}
	if entry.Version != registryProbeCacheVersion {
		return registrymanifest.Endpoint{}, false, false
	}
	if entry.Runtime != want.runtime || entry.Image != want.image || entry.NetworkMode != want.networkMode {
		return registrymanifest.Endpoint{}, false, false
	}
	switch entry.Transport {
	case "unix":
		return registrymanifest.NewUnixEndpoint(""), entry.TCPAddHost, true
	case "tcp":
		if entry.TCPHost == "" {
			return registrymanifest.Endpoint{}, false, false
		}
		return registrymanifest.NewTCPEndpoint(entry.TCPHost, ""), entry.TCPAddHost, true
	default:
		return registrymanifest.Endpoint{}, false, false
	}
}

// storeRegistryProbeCache remembers a fresh probe verdict for key. pwd == ""
// is a silent no-op, and an endpoint that is neither unix nor tcp is
// rejected. Given a non-empty pwd it writes unconditionally: the "no registry
// proxy configured, no file written" guarantee is the caller's, today
// dispatch/box.go's route-count gate, and #3114's doctor row must repeat it.
func storeRegistryProbeCache(pwd string, key registryProbeCacheKey, endpoint registrymanifest.Endpoint, tcpAddHost bool) error {
	path := registryProbeCachePath(pwd)
	if path == "" {
		return nil
	}
	entry := registryProbeCacheEntry{
		Version:     registryProbeCacheVersion,
		Runtime:     key.runtime,
		Image:       key.image,
		NetworkMode: key.networkMode,
		TCPAddHost:  tcpAddHost,
	}
	switch {
	case endpoint.IsUnix():
		entry.Transport = "unix"
	case endpoint.IsTCP():
		entry.Transport = "tcp"
		entry.TCPHost = endpoint.Host()
	default:
		return errors.New("registryprobecache: refusing to store an endpoint that is neither unix nor tcp")
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("registryprobecache: marshaling cache entry: %w", err)
	}
	cacheDir := filepath.Dir(path)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("registryprobecache: creating cache dir: %w", err)
	}
	// Rename into place rather than writing path directly: os.WriteFile
	// truncates first, so a second dispatch reading mid-write, or a crash,
	// leaves a torn file. The rename stays in one directory, so it is an
	// atomic replace.
	tmp, err := os.CreateTemp(cacheDir, ".registry-probe-cache-*.tmp")
	if err != nil {
		return fmt.Errorf("registryprobecache: creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("registryprobecache: writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("registryprobecache: closing temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("registryprobecache: renaming cache file into place: %w", err)
	}
	return nil
}
