package ghapptoken

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeMintScript echoes a deterministic token built from the env vars Mint
// wires through, so tests can assert on env-wiring without any real
// network/openssl/curl/jq call. It fails loudly (matching the real script's
// shape) when the "key file" doesn't exist, so error-path tests can drive
// that by pointing PrivateKeyFile at a nonexistent path.
const fakeMintScript = `#!/usr/bin/env bash
set -euo pipefail
: "${GH_APP_ID:?GH_APP_ID must be set}"
: "${GH_APP_PRIVATE_KEY_FILE:?GH_APP_PRIVATE_KEY_FILE must be set}"
: "${GH_APP_INSTALLATION_ID:?GH_APP_INSTALLATION_ID must be set}"
if [ ! -f "$GH_APP_PRIVATE_KEY_FILE" ]; then
  echo "fake mint: key file not found: $GH_APP_PRIVATE_KEY_FILE" >&2
  exit 1
fi
printf 'minted-%s-%s' "$GH_APP_ID" "$GH_APP_INSTALLATION_ID"
`

func TestMintWithScript_WiresEnvAndReturnsTrimmedToken(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(keyFile, []byte("fake-key"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg := Config{
		AppID:          "123",
		PrivateKeyFile: keyFile,
		InstallationID: "456",
	}

	token, err := mintWithScript(context.Background(), []byte(fakeMintScript), cfg)
	if err != nil {
		t.Fatalf("mintWithScript returned error: %v", err)
	}

	want := "minted-123-456"
	if token != want {
		t.Errorf("token = %q, want %q", token, want)
	}
}

func TestMintWithScript_SurfacesScriptStderrOnFailure(t *testing.T) {
	cfg := Config{
		AppID:          "123",
		PrivateKeyFile: filepath.Join(t.TempDir(), "does-not-exist.pem"),
		InstallationID: "456",
	}

	_, err := mintWithScript(context.Background(), []byte(fakeMintScript), cfg)
	if err == nil {
		t.Fatal("mintWithScript returned nil error, want an error surfacing script stderr")
	}

	const wantSubstr = "key file not found"
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("error = %q, want it to contain %q", err.Error(), wantSubstr)
	}
}

func TestMintWithScript_EmptyAppIDSurfacesScriptValidationError(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(keyFile, []byte("fake-key"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg := Config{
		AppID:          "",
		PrivateKeyFile: keyFile,
		InstallationID: "456",
	}

	_, err := mintWithScript(context.Background(), []byte(fakeMintScript), cfg)
	if err == nil {
		t.Fatal("mintWithScript returned nil error, want an error from the script's own GH_APP_ID validation")
	}

	const wantSubstr = "GH_APP_ID must be set"
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("error = %q, want it to contain %q", err.Error(), wantSubstr)
	}
}

func TestMint_UsesEmbeddedRealScript(t *testing.T) {
	// Mint (the public entry point) must invoke the actual embedded
	// mint-token.sh, not just any script — exercise it far enough to prove
	// that wiring, without needing real openssl/curl/jq/GitHub API access:
	// an empty AppID trips the embedded script's own GH_APP_ID validation,
	// which only the real script content can produce.
	cfg := Config{
		AppID:          "",
		PrivateKeyFile: filepath.Join(t.TempDir(), "key.pem"),
		InstallationID: "456",
	}

	_, err := Mint(context.Background(), cfg)
	if err == nil {
		t.Fatal("Mint returned nil error, want an error from the embedded script's GH_APP_ID validation")
	}

	const wantSubstr = "GH_APP_ID must be set"
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("error = %q, want it to contain %q", err.Error(), wantSubstr)
	}
}
