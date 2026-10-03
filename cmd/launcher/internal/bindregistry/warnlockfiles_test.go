package bindregistry

import (
	"bytes"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registrymanifest"
)

func setManifestOn(t *testing.T) {
	t.Helper()
	m := registrymanifest.Manifest{Endpoint: registrymanifest.NewUnixEndpoint(filepath.Join(t.TempDir(), "p.sock"))}
	encoded, err := registrymanifest.Encode(m)
	if err != nil {
		t.Fatalf("registrymanifest.Encode: %v", err)
	}
	t.Setenv(registrymanifest.EnvVar, encoded)
}

func commitCargoLock(t *testing.T, source string) string {
	t.Helper()
	dir := newTestRepo(t)
	writeTrackedFile(t, dir, "Cargo.lock", []byte("source = \""+source+"\"\n"))
	runGit(t, dir, "commit", "-m", "add lockfile")
	return dir
}

func TestWarnStaleLockfilesWarnsOnHit(t *testing.T) {
	port := strconv.Itoa(ForwarderPort)
	dir := commitCargoLock(t, "registry+http://127.0.0.1:"+port+"/")
	setManifestOn(t)

	var out bytes.Buffer
	WarnStaleLockfiles(&out, dir)

	want := "==> WARNING: cargo lockfile Cargo.lock still names the registry proxy Forwarder URL 127.0.0.1:" + port + " — this will ship in the PR (issue #3199)\n"
	if out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// A clean run never gets "scanned N lockfiles" chatter.
func TestWarnStaleLockfilesCleanRepoIsSilent(t *testing.T) {
	dir := commitCargoLock(t, "registry+https://index.crates.io/")
	setManifestOn(t)

	var out bytes.Buffer
	WarnStaleLockfiles(&out, dir)

	if out.Len() != 0 {
		t.Errorf("output = %q, want empty for a clean repo", out.String())
	}
}

// An off-dispatch is never scanned, whatever a tracked lockfile contains.
func TestWarnStaleLockfilesManifestAbsentIsSilent(t *testing.T) {
	dir := commitCargoLock(t, "registry+http://127.0.0.1:"+strconv.Itoa(ForwarderPort)+"/")
	t.Setenv(registrymanifest.EnvVar, "")

	var out bytes.Buffer
	WarnStaleLockfiles(&out, dir)

	if out.Len() != 0 {
		t.Errorf("output = %q, want empty when the registry proxy is off", out.String())
	}
}

func TestWarnStaleLockfilesMalformedManifestWarns(t *testing.T) {
	t.Setenv(registrymanifest.EnvVar, "{not valid json")

	var out bytes.Buffer
	WarnStaleLockfiles(&out, newTestRepo(t))

	want := "==> WARNING: REGISTRY_PROXY_MANIFEST is malformed, skipping the lockfile Forwarder-URL scan:"
	if !strings.HasPrefix(out.String(), want) {
		t.Errorf("output = %q, want it to start with %q", out.String(), want)
	}
}

// git ls-files failing because the work dir is not a repo must warn, not fail.
func TestWarnStaleLockfilesScanErrorWarns(t *testing.T) {
	setManifestOn(t)

	var out bytes.Buffer
	WarnStaleLockfiles(&out, t.TempDir())

	want := "==> WARNING: lockfile Forwarder-URL scan failed, skipping:"
	if !strings.HasPrefix(out.String(), want) {
		t.Errorf("output = %q, want it to start with %q", out.String(), want)
	}
}
