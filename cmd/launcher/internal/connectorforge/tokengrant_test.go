package connectorforge

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
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
		return json.Unmarshal([]byte(`{"introspectable":true,"writeCapable":true,"scopes":["read:org","repo"]}`), result)
	}
	got, err := introspectToken(checked(call), []string{capTokenGrant}, testTarget, "ghp_SECRET")
	if err != nil || !reflect.DeepEqual(got, forge.TokenIntrospection{Introspectable: true, WriteCapable: true, Scopes: []string{"read:org", "repo"}}) {
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
	got, err := introspectToken(checked(call), []string{capTokenGrant}, testTarget, "github_pat_x")
	if err != nil || !reflect.DeepEqual(got, forge.TokenIntrospection{Reason: "why"}) {
		t.Fatalf("introspectToken = %+v, %v", got, err)
	}
}

func TestIntrospectTokenUnadvertisedIsErrNoTokenGrant(t *testing.T) {
	call := func(context.Context, string, any, any) error {
		t.Fatal("called a Connector that does not declare forge.tokenGrant")
		return nil
	}
	_, err := introspectToken(checked(call), []string{capProbe}, testTarget, "ghp_x")
	if !errors.Is(err, ErrNoTokenGrant) {
		t.Fatalf("err = %v, want ErrNoTokenGrant", err)
	}
}

func TestIntrospectTokenPassesTheConnectorsErrorThrough(t *testing.T) {
	boom := errors.New("boom")
	call := func(context.Context, string, any, any) error { return boom }
	_, err := introspectToken(checked(call), []string{capTokenGrant}, testTarget, "ghp_x")
	if !errors.Is(err, boom) || errors.Is(err, ErrNoTokenGrant) {
		t.Fatalf("err = %v, want the Connector's own error", err)
	}
}

func grantCall(raw string) guestCall {
	return func(_ context.Context, _ string, _, result any) error {
		return json.Unmarshal([]byte(raw), result)
	}
}

func TestIntrospectTokenRefusesAnInconsistentGrant(t *testing.T) {
	for name, raw := range map[string]string{
		"write without introspectable":  `{"introspectable":false,"writeCapable":true}`,
		"scopes without introspectable": `{"introspectable":false,"scopes":["repo"]}`,
		"empty scope":                   `{"introspectable":true,"scopes":["repo",""]}`,
		"scope with a space":            `{"introspectable":true,"scopes":["repo admin"]}`,
		"scope with an escape":          `{"introspectable":true,"scopes":["repo\u001b[31m"]}`,
		"scope with a newline":          `{"introspectable":true,"scopes":["repo\nforged: ok"]}`,
		"overlong scope":                `{"introspectable":true,"scopes":["` + strings.Repeat("a", maxScopeLen+1) + `"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := introspectToken(checked(grantCall(raw)), []string{capTokenGrant}, testTarget, "ghp_x")
			if !errors.Is(err, ErrAnswer) {
				t.Errorf("err = %v, want ErrAnswer", err)
			}
		})
	}
}

func TestIntrospectTokenAcceptsRealScopes(t *testing.T) {
	raw := `{"introspectable":true,"writeCapable":true,"scopes":["repo","public_repo","write:repository","read:org","admin:repo_hook"]}`
	got, err := introspectToken(checked(grantCall(raw)), []string{capTokenGrant}, testTarget, "ghp_x")
	if err != nil || len(got.Scopes) != 5 || !got.WriteCapable {
		t.Fatalf("introspectToken = %+v, %v", got, err)
	}
}

func TestIntrospectTokenSanitisesAndCapsTheReason(t *testing.T) {
	raw := `{"introspectable":false,"reason":"\u001b[2Jfine\nINJECTED\u0007‮ ` + strings.Repeat("x", 2*maxReasonLen) + `"}`
	got, err := introspectToken(checked(grantCall(raw)), []string{capTokenGrant}, testTarget, "ghp_x")
	if err != nil {
		t.Fatal(err)
	}
	if strings.IndexFunc(got.Reason, unicode.IsControl) >= 0 {
		t.Errorf("reason %q keeps a control character", got.Reason)
	}
	if n := utf8.RuneCountInString(got.Reason); n > maxReasonLen {
		t.Errorf("reason has %d runes, want at most %d", n, maxReasonLen)
	}
	if !strings.HasPrefix(got.Reason, "fine INJECTED ") {
		t.Errorf("reason = %q", got.Reason)
	}
}

// checked runs a stub guest through the forge's result checks, as every
// Connector call does.
func checked(call guestCall) guestCall {
	return pushOnlyOver(auditStub{}, call, retry.Clock{}, testTarget).call
}
