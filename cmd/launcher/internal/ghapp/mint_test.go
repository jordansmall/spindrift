package ghapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testKeyPEM generates a fresh RSA key and returns it PKCS#1 PEM-encoded
// alongside the key, so a test can both feed the Minter and verify the JWT
// signature it produces.
func testKeyPEM(t *testing.T) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return string(pemBytes), key
}

// verifyJWT checks the RS256 signature against pub and returns the decoded
// claims, failing the test on any malformed segment or bad signature.
func verifyJWT(t *testing.T, token string, pub *rsa.PublicKey) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d segments, want 3", len(parts))
	}
	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("jwt signature invalid: %v", err)
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	return claims
}

func TestMintSignsJWTAndReturnsToken(t *testing.T) {
	keyPEM, key := testKeyPEM(t)
	const wantToken = "ghs_installationtoken"
	wantExpiry := time.Now().Add(1 * time.Hour).UTC().Round(time.Second)

	var sawInstallCall, sawTokenCall bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Every call must present the App JWT as a Bearer token; verify it
		// against the App public key and its iss claim.
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("%s %s: missing Bearer auth, got %q", r.Method, r.URL.Path, auth)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		claims := verifyJWT(t, strings.TrimPrefix(auth, "Bearer "), &key.PublicKey)
		if claims["iss"] != "12345" {
			t.Errorf("jwt iss = %v, want 12345", claims["iss"])
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/installation":
			sawInstallCall = true
			json.NewEncoder(w).Encode(map[string]any{"id": 999})
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/999/access_tokens":
			sawTokenCall = true
			json.NewEncoder(w).Encode(map[string]any{
				"token":      wantToken,
				"expires_at": wantExpiry.Format(time.RFC3339),
			})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	m, err := NewMinter(Config{
		AppID:         "12345",
		PrivateKeyPEM: keyPEM,
		Repo:          "o/r",
		APIBaseURL:    srv.URL,
		HTTPClient:    srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewMinter: %v", err)
	}

	token, expiry, err := m.Mint(context.Background())
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if token != wantToken {
		t.Errorf("token = %q, want %q", token, wantToken)
	}
	if !expiry.Equal(wantExpiry) {
		t.Errorf("expiry = %v, want %v", expiry, wantExpiry)
	}
	if !sawInstallCall || !sawTokenCall {
		t.Errorf("expected both API calls; install=%v token=%v", sawInstallCall, sawTokenCall)
	}
}

func TestMintPropagatesAPIError(t *testing.T) {
	keyPEM, _ := testKeyPEM(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	m, err := NewMinter(Config{
		AppID: "1", PrivateKeyPEM: keyPEM, Repo: "o/r",
		APIBaseURL: srv.URL, HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewMinter: %v", err)
	}
	if _, _, err := m.Mint(context.Background()); err == nil {
		t.Fatal("expected error on 401, got nil")
	}
}

func TestNewMinterRejectsBadInput(t *testing.T) {
	keyPEM, _ := testKeyPEM(t)
	cases := map[string]Config{
		"no app id":  {PrivateKeyPEM: keyPEM, Repo: "o/r"},
		"no key":     {AppID: "1", Repo: "o/r"},
		"no repo":    {AppID: "1", PrivateKeyPEM: keyPEM},
		"bad key":    {AppID: "1", PrivateKeyPEM: "not a pem", Repo: "o/r"},
		"garbage pem": {AppID: "1", PrivateKeyPEM: "-----BEGIN RSA PRIVATE KEY-----\nZm9v\n-----END RSA PRIVATE KEY-----", Repo: "o/r"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewMinter(cfg); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

// TestNewMinterAcceptsPKCS8 confirms a PKCS#8 ("PRIVATE KEY") PEM parses too,
// not just GitHub's default PKCS#1.
func TestNewMinterAcceptsPKCS8(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if _, err := NewMinter(Config{AppID: "1", PrivateKeyPEM: pemStr, Repo: "o/r"}); err != nil {
		t.Fatalf("NewMinter with PKCS#8 key: %v", err)
	}
}

// Guard against the error path leaking the JWT into the surfaced message.
func TestMintErrorOmitsJWT(t *testing.T) {
	keyPEM, _ := testKeyPEM(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	m, _ := NewMinter(Config{AppID: "1", PrivateKeyPEM: keyPEM, Repo: "o/r", APIBaseURL: srv.URL, HTTPClient: srv.Client()})
	_, _, err := m.Mint(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "Bearer ") || strings.Contains(err.Error(), "eyJ") {
		t.Fatalf("error message leaked JWT: %v", err)
	}
}
