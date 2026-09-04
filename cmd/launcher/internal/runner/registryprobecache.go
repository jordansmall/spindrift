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
// future field change makes an old file miss cleanly (loadRegistryProbeCache
// rejects any version but this one) rather than being misread as the new
// shape.
const registryProbeCacheVersion = 1

// registryProbeCacheKey is the set of inputs RegistryProxyTransport's
// verdict actually depends on: the container runtime binary and the image
// (#3120: an old image missing the probe-registry-socket verb reads as
// socket-incapable, so the image ref must invalidate a stale verdict too),
// plus the network mode (oci.go's socket-incapable path hard-errors under a
// host-loopback-denying mode rather than falling back to TCP, so a verdict
// cached under one mode must not replay under another). A load whose stored
// key differs from the live key is a miss, never a silently-wrong answer.
//
// runtime holds the resolved runtime binary (a.cli, BinaryFor's result), not
// the raw infra.runtime string, so the "rancher" and "nerdctl" aliases that
// resolve to the same binary share one cache entry -- the correct
// granularity, since the verdict depends on the binary actually executed,
// not the name the operator typed.
type registryProbeCacheKey struct {
	runtime     string
	image       string
	networkMode string
}

// registryProbeCacheEntry is the on-disk payload: the key the verdict was
// probed under, plus the verdict itself in its own shape rather than an
// embedded registrymanifest.Endpoint -- Endpoint's JSON codec renders/parses
// the ADR-0045 "unix://"/"tcp://host:port" string form, which rejects the
// path-less/port-less Endpoint values the probe actually returns (the real
// path/port is minted by the caller afterwards).
type registryProbeCacheEntry struct {
	Version     int    `json:"version"`
	Runtime     string `json:"runtime"`
	Image       string `json:"image"`
	NetworkMode string `json:"networkMode"`
	Transport   string `json:"transport"`
	TCPHost     string `json:"tcpHost,omitempty"`
	TCPAddHost  bool   `json:"tcpAddHost"`
}

// registryProbeCachePath returns the fixed, single-file cache path for pwd,
// or "" when pwd is "" -- the signal loadRegistryProbeCache and
// storeRegistryProbeCache both use to disable the cache entirely (a launcher
// with no working directory has nowhere legitimate to write, and every
// existing probe test constructs an adapter with no pwd). The path is fixed
// rather than keyed into the filename so the operator force-re-probe gesture
// is "delete this one documented file", not a glob over opaque names.
func registryProbeCachePath(pwd string) string {
	if pwd == "" {
		return ""
	}
	return filepath.Join(pwd, ".spindrift", "registry-probe-cache.json")
}

// loadRegistryProbeCache returns the remembered transport decision for want,
// or ok=false on any miss: no pwd, no file, an unreadable file, malformed
// JSON, an unrecognised version or transport, a stored key that differs from
// want, or a stored tcp entry with no host (an unusable decision). This
// mirrors the freshness Guard's corruption-tolerance idiom -- every miss
// path falls through to a fresh probe rather than surfacing an error, so a
// damaged cache file can never fail a dispatch.
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

// storeRegistryProbeCache remembers a fresh probe verdict for key, in its
// own shape (transport kind + tcp host) rather than endpoint's ADR-0045
// string form -- see registryProbeCacheEntry. pwd == "" is a silent no-op,
// matching loadRegistryProbeCache's cache-disabled behavior. An endpoint
// that is neither unix nor tcp is a caller bug, not a coherent verdict to
// persist, so it is rejected rather than written.
//
// This function itself writes unconditionally given a non-empty pwd --
// the "no registry proxy configured, no file written" guarantee is the
// caller's to hold, today via dispatch/box.go's
// len(d.cfg.RegistryProxyRoutes) > 0 gate around its one call site. A future
// second caller (the #3114 doctor row) must replicate that gate itself.
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
	// Write to a temp file in the same directory and rename it into place
	// rather than writing path directly: os.WriteFile truncates path before
	// writing the new bytes, leaving a window where a concurrent reader (a
	// second dispatch, or a crash mid-write) sees a torn, truncated file.
	// Renaming within the same directory is a same-filesystem, atomic
	// replace, so a reader always sees either the whole old file or the
	// whole new one.
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
