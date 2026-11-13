package connectorforge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/backendkit"
)

func TestTokenPrefix(t *testing.T) {
	for token, want := range map[string]string{
		"ghp_abc123":      "ghp_",
		"github_pat_abc":  "github_",
		"ghs_x_y":         "ghs_",
		"opaque":          "",
		"":                "",
		"_leading":        "_",
		"trailing_":       "trailing_",
		"no-underscore-1": "",
	} {
		if got := tokenPrefix(token); got != want {
			t.Errorf("tokenPrefix(%q) = %q, want %q", token, got, want)
		}
	}
}

func TestIntrospectTokenSendsOnlyThePrefix(t *testing.T) {
	var method string
	var sent []byte
	call := func(_ context.Context, m string, params, result any) error {
		method = m
		sent, _ = json.Marshal(params)
		return json.Unmarshal([]byte(`{"introspectable":true,"writeCapable":true}`), result)
	}
	got, err := introspectToken(call, []string{capTokenGrant}, testTarget, "ghp_SECRET")
	if err != nil || got != (backendkit.TokenIntrospection{Introspectable: true, WriteCapable: true}) {
		t.Fatalf("introspectToken = %+v, %v", got, err)
	}
	if method != "forge.tokenGrant" {
		t.Errorf("method = %q", method)
	}
	var p struct {
		Target      Target `json:"target"`
		TokenPrefix string `json:"tokenPrefix"`
	}
	if err := json.Unmarshal(sent, &p); err != nil || p.Target != testTarget || p.TokenPrefix != "ghp_" {
		t.Errorf("params = %s (%v), want the target and prefix ghp_", sent, err)
	}
	if string(sent) != `{"target":{"api":"`+testTarget.API+`","repo":"`+testTarget.Repo+`"},"tokenPrefix":"ghp_"}` {
		t.Errorf("params = %s carry more than the target and prefix", sent)
	}
}

func TestIntrospectTokenCarriesTheReason(t *testing.T) {
	call := func(_ context.Context, _ string, _, result any) error {
		return json.Unmarshal([]byte(`{"introspectable":false,"writeCapable":false,"reason":"why"}`), result)
	}
	got, err := introspectToken(call, []string{capTokenGrant}, testTarget, "github_pat_x")
	if err != nil || got != (backendkit.TokenIntrospection{Reason: "why"}) {
		t.Fatalf("introspectToken = %+v, %v", got, err)
	}
}

func TestIntrospectTokenUnadvertisedIsErrNoTokenGrant(t *testing.T) {
	call := func(context.Context, string, any, any) error {
		t.Fatal("called a Connector that does not declare forge.tokenGrant")
		return nil
	}
	_, err := introspectToken(call, []string{capProbe}, testTarget, "ghp_x")
	if !errors.Is(err, ErrNoTokenGrant) {
		t.Fatalf("err = %v, want ErrNoTokenGrant", err)
	}
}

func TestIntrospectTokenPassesTheConnectorsErrorThrough(t *testing.T) {
	boom := errors.New("boom")
	call := func(context.Context, string, any, any) error { return boom }
	_, err := introspectToken(call, []string{capTokenGrant}, testTarget, "ghp_x")
	if !errors.Is(err, boom) || errors.Is(err, ErrNoTokenGrant) {
		t.Fatalf("err = %v, want the Connector's own error", err)
	}
}
