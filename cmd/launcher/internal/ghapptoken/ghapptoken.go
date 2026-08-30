// Package ghapptoken mints a GitHub App installation token by execing the
// embedded mint-token.sh recipe — the single source of truth for this
// recipe, shared with .github/actions/gh-token-refresher (CI). See
// mint-token.sh for the JWT-signing + token-exchange details (issue #2867).
package ghapptoken

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

//go:embed mint-token.sh
var mintScriptEmbed []byte

// remintFailureBackoff is how long the periodic re-mint loop in
// watchWithScript waits before retrying after a failed re-mint or a failed
// token-file write, rather than waiting out the full (much longer) interval.
// Mirrors CI's sibling loop (.github/actions/gh-token-refresher/action.yml),
// which drops to a 5-minute retry (sleep_secs=300) on a failed mint attempt
// and resets to the full interval once a re-mint succeeds. A var, not a
// const, so tests in this package can override it to keep failure-path
// tests fast.
var remintFailureBackoff = 5 * time.Minute

// Config holds the GitHub App identity and target installation to mint a
// fresh installation token for.
type Config struct {
	// AppID is the GitHub App's numeric ID.
	AppID string
	// PrivateKeyFile is the path to the App's PEM private key.
	PrivateKeyFile string
	// InstallationID is the installation to mint a token for.
	InstallationID string
}

// Mint execs the embedded mint-token.sh recipe with cfg's fields exported as
// GH_APP_ID/GH_APP_PRIVATE_KEY_FILE/GH_APP_INSTALLATION_ID and returns the
// freshly minted installation token.
func Mint(ctx context.Context, cfg Config) (string, error) {
	return mintWithScript(ctx, mintScriptEmbed, cfg)
}

// mintWithScript runs script (bash source, fed via stdin) with cfg's fields
// exported as env vars, returning the trimmed stdout as the token. Split out
// from Mint so tests can swap in a fake script and exercise the env-wiring,
// success, and error paths without any real network/openssl/curl/jq call.
func mintWithScript(ctx context.Context, script []byte, cfg Config) (string, error) {
	cmd := exec.CommandContext(ctx, "bash", "-s")
	cmd.Stdin = bytes.NewReader(script)
	cmd.Env = append(os.Environ(),
		"GH_APP_ID="+cfg.AppID,
		"GH_APP_PRIVATE_KEY_FILE="+cfg.PrivateKeyFile,
		"GH_APP_INSTALLATION_ID="+cfg.InstallationID,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("mint GitHub App installation token: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	return strings.TrimSpace(stdout.String()), nil
}

// Watch mints an initial GitHub App installation token, writes it to
// tokenFile, then re-mints every interval and atomically rewrites the file
// in place until stop is closed -- the producer side that feeds
// tokenrefresh.Watch's existing file-poll consumer (cmd/launcher/internal/
// tokenrefresh), so local dispatch reuses that consumer unchanged rather
// than adding a second one (issue #2867).
//
// The first mint+write happens synchronously: Watch returns the freshly
// minted token alongside a non-nil error immediately if it fails, so a
// caller can both fail fast on bad App credentials rather than silently
// limping with an empty GH_TOKEN, and populate its own in-memory token
// field (e.g. bootstrap.go's config.ghToken) without a second mint or a
// re-read of tokenFile. Once that first write succeeds, Watch starts the
// periodic re-mint loop in its own goroutine and returns without waiting on
// the loop's lifetime.
func Watch(ctx context.Context, cfg Config, tokenFile string, interval time.Duration, stop <-chan struct{}) (string, error) {
	return watchWithScript(ctx, mintScriptEmbed, cfg, tokenFile, interval, stop)
}

// watchWithScript is Watch's testable core: script is threaded through to
// every mint call (including the periodic re-mints), so tests can swap in a
// fake script the same way mintWithScript's tests do.
func watchWithScript(ctx context.Context, script []byte, cfg Config, tokenFile string, interval time.Duration, stop <-chan struct{}) (string, error) {
	token, err := mintWithScript(ctx, script, cfg)
	if err != nil {
		return "", err
	}
	if err := writeTokenFileAtomic(tokenFile, token); err != nil {
		return "", err
	}

	go func() {
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-stop:
				return
			case <-timer.C:
				// A transient mint or write failure doesn't wait out the
				// full interval -- it gets logged and retried after a short
				// backoff instead, since the consumer (tokenrefresh.Watch)
				// keeps using the last-good token already on disk until a
				// re-mint succeeds, but the installation token itself only
				// lives ~1h, so a long silent wait risks it going dead.
				next, err := mintWithScript(ctx, script, cfg)
				if err != nil {
					fmt.Fprintf(os.Stderr, "ghapptoken: periodic re-mint failed, retrying in %s: %v\n", remintFailureBackoff, err)
					timer.Reset(remintFailureBackoff)
					continue
				}
				if err := writeTokenFileAtomic(tokenFile, next); err != nil {
					fmt.Fprintf(os.Stderr, "ghapptoken: writing re-minted token file failed, retrying in %s: %v\n", remintFailureBackoff, err)
					timer.Reset(remintFailureBackoff)
					continue
				}
				timer.Reset(interval)
			}
		}
	}()

	return token, nil
}

// writeTokenFileAtomic writes token to tokenFile by writing to a sibling
// ".tmp" file and renaming it into place, so a concurrent reader (tokenrefresh.
// Watch's poll loop) never observes a partially written file. Mode 0o600
// since this is a live credential.
func writeTokenFileAtomic(tokenFile, token string) error {
	tmp := tokenFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(token), 0o600); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	if err := os.Rename(tmp, tokenFile); err != nil {
		return fmt.Errorf("rename token file into place: %w", err)
	}
	return nil
}
