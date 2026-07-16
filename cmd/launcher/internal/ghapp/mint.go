// Package ghapp mints short-lived GitHub App installation tokens and keeps
// the ambient GH_TOKEN fresh for the life of a long dispatch run.
//
// A dispatch run can outlive the ~1h installation token minted once at
// workflow start (issue #1027): the Box agent runs for an unbounded time and
// the host-side terminal GitHub operations (CI poll, gh pr merge, label
// edits, final comment) all shell out to gh, which reads GH_TOKEN from the
// ambient process environment. Rather than thread a token through every gh
// call site, the Refresher re-mints from the App private key and republishes
// GH_TOKEN via os.Setenv before each expiry, so every later gh subprocess
// inherits a valid token automatically.
//
// The App private key stays on the launcher host (it is never a boxEnv knob,
// so buildBoxEnv does not forward it into the Box container) — only the
// short-lived installation token ever reaches a Box.
package ghapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultAPIBaseURL is GitHub's REST API root; spindrift targets github.com.
const defaultAPIBaseURL = "https://api.github.com"

// Minter mints installation access tokens for a GitHub App against one repo.
// It signs a short-lived App JWT with the App private key, discovers the
// installation on the target repo, and exchanges the JWT for an installation
// token.
type Minter struct {
	appID      string
	repo       string // owner/repo
	key        *rsa.PrivateKey
	apiBaseURL string
	httpClient *http.Client
	now        func() time.Time
}

// Config carries the inputs a Minter needs. AppID and PrivateKeyPEM are the
// GitHub App credentials (repo secrets on the runner); Repo is the owner/repo
// slug the token is scoped to. APIBaseURL and HTTPClient are for tests; empty
// / nil select the github.com defaults.
type Config struct {
	AppID         string
	PrivateKeyPEM string
	Repo          string
	APIBaseURL    string
	HTTPClient    *http.Client
	Now           func() time.Time
}

// NewMinter parses the PEM private key and returns a ready Minter. It errors
// if any credential is missing or the key does not parse as an RSA private
// key (PKCS#1 or PKCS#8).
func NewMinter(cfg Config) (*Minter, error) {
	if cfg.AppID == "" {
		return nil, fmt.Errorf("ghapp: app ID is empty")
	}
	if cfg.PrivateKeyPEM == "" {
		return nil, fmt.Errorf("ghapp: private key is empty")
	}
	if cfg.Repo == "" {
		return nil, fmt.Errorf("ghapp: repo is empty")
	}
	key, err := parseRSAPrivateKey(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	m := &Minter{
		appID:      cfg.AppID,
		repo:       cfg.Repo,
		key:        key,
		apiBaseURL: cfg.APIBaseURL,
		httpClient: cfg.HTTPClient,
		now:        cfg.Now,
	}
	if m.apiBaseURL == "" {
		m.apiBaseURL = defaultAPIBaseURL
	}
	if m.httpClient == nil {
		m.httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if m.now == nil {
		m.now = time.Now
	}
	return m, nil
}

// Mint returns a fresh installation token for the configured repo and the
// instant it expires. It signs an App JWT, resolves the repo's installation
// id, and exchanges the JWT for an installation token.
func (m *Minter) Mint(ctx context.Context) (token string, expiresAt time.Time, err error) {
	jwt, err := m.signJWT()
	if err != nil {
		return "", time.Time{}, err
	}
	installID, err := m.installationID(ctx, jwt)
	if err != nil {
		return "", time.Time{}, err
	}
	return m.installationToken(ctx, jwt, installID)
}

// signJWT builds and RS256-signs the App authentication JWT. iat is backdated
// 60s to tolerate clock drift between the runner and GitHub; exp is 9 minutes
// out (GitHub caps App JWTs at 10 minutes).
func (m *Minter) signJWT() (string, error) {
	now := m.now()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": m.appID,
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, m.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("ghapp: sign JWT: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// installationID resolves the App's installation on the target repo.
func (m *Minter) installationID(ctx context.Context, jwt string) (int64, error) {
	url := fmt.Sprintf("%s/repos/%s/installation", m.apiBaseURL, m.repo)
	var body struct {
		ID int64 `json:"id"`
	}
	if err := m.apiGet(ctx, url, jwt, &body); err != nil {
		return 0, fmt.Errorf("ghapp: resolve installation for %s: %w", m.repo, err)
	}
	if body.ID == 0 {
		return 0, fmt.Errorf("ghapp: no installation found for %s", m.repo)
	}
	return body.ID, nil
}

// installationToken exchanges the App JWT for a scoped installation token.
func (m *Minter) installationToken(ctx context.Context, jwt string, installID int64) (string, time.Time, error) {
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", m.apiBaseURL, installID)
	var body struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := m.apiPost(ctx, url, jwt, &body); err != nil {
		return "", time.Time{}, fmt.Errorf("ghapp: mint installation token: %w", err)
	}
	if body.Token == "" {
		return "", time.Time{}, fmt.Errorf("ghapp: installation token response had no token")
	}
	return body.Token, body.ExpiresAt, nil
}

func (m *Minter) apiGet(ctx context.Context, url, jwt string, out any) error {
	return m.apiDo(ctx, http.MethodGet, url, jwt, out)
}

func (m *Minter) apiPost(ctx context.Context, url, jwt string, out any) error {
	return m.apiDo(ctx, http.MethodPost, url, jwt, out)
}

// apiDo issues one App-JWT-authenticated request and decodes a 2xx JSON body
// into out. Non-2xx responses become errors carrying the status and a bounded
// slice of the body for diagnosis (never the JWT).
func (m *Minter) apiDo(ctx context.Context, method, url, jwt string, out any) error {
	var reqBody io.Reader
	if method == http.MethodPost {
		reqBody = bytes.NewReader([]byte("{}"))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode %s response: %w", url, err)
		}
	}
	return nil
}

// parseRSAPrivateKey accepts a PEM-encoded RSA private key in either PKCS#1
// ("RSA PRIVATE KEY", GitHub's default) or PKCS#8 ("PRIVATE KEY") form.
func parseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("ghapp: private key is not valid PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ghapp: parse private key: %w", err)
	}
	key, ok := keyAny.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("ghapp: private key is not RSA (%T)", keyAny)
	}
	return key, nil
}
