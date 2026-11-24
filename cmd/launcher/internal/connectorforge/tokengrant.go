package connectorforge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/retry"
)

// ErrNoTokenGrant marks a Connector that does not declare "forge.tokenGrant":
// its token cannot be introspected, which is not a failed introspection.
var ErrNoTokenGrant = errors.New("connector does not declare forge.tokenGrant")

// tokenPrefix is the token's non-secret leading part, through its first "_",
// "" when it has none. It is all a Connector is told of the token it audits,
// which the host attaches as the Connector's credential.
func tokenPrefix(token string) string {
	i := strings.IndexByte(token, '_')
	if i < 0 {
		return ""
	}
	return token[:i+1]
}

// IntrospectToken asks c what token may do on t, in canonical terms. The host
// must have loaded c with token as its credential: the request carries only
// the token's prefix. It returns ErrNoTokenGrant when c does not declare
// "forge.tokenGrant".
func IntrospectToken(c *connectorhost.Connector, t Target, token string) (backendkit.TokenIntrospection, error) {
	return introspectToken(newPushOnly(c, t, retry.Clock{}).call, c.Capabilities, t, token)
}

func introspectToken(call guestCall, caps []string, t Target, token string) (backendkit.TokenIntrospection, error) {
	if !slices.Contains(caps, capTokenGrant) {
		return backendkit.TokenIntrospection{}, ErrNoTokenGrant
	}
	var g wireGrant
	if err := call(context.Background(), "forge.tokenGrant", tokenGrantParams{t, tokenPrefix(token)}, &g); err != nil {
		return backendkit.TokenIntrospection{}, err
	}
	return backendkit.TokenIntrospection(g), nil
}

const (
	maxScopeLen  = 64
	maxReasonLen = 200
)

// checkGrant refuses a grant whose fields contradict each other or whose
// scopes are not plain tokens: core prints scopes and acts on writeCapable,
// so neither may reach it unchecked from a Connector.
func checkGrant(g wireGrant) error {
	if !g.Introspectable && g.WriteCapable {
		return fmt.Errorf("%w: forge.tokenGrant reports writeCapable without introspectable", ErrAnswer)
	}
	if !g.Introspectable && len(g.Scopes) > 0 {
		return fmt.Errorf("%w: forge.tokenGrant reports scopes without introspectable", ErrAnswer)
	}
	for _, s := range g.Scopes {
		if !wellFormedScope(s) {
			return fmt.Errorf("%w: forge.tokenGrant scope %q is not a well-formed token", ErrAnswer, s)
		}
	}
	return nil
}

// wellFormedScope admits the charset real forges use (repo, write:repository,
// admin:repo_hook), bounded in length.
func wellFormedScope(s string) bool {
	if s == "" || len(s) > maxScopeLen {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(":._/-", r))
	})
}

// cleanReason flattens line breaks to spaces, strips what SanitizeLine strips
// (whole ANSI sequences, controls, bidi and other format runes), then caps the
// runes: the write gate prints it verbatim.
func cleanReason(s string) string {
	s = driverkit.SanitizeLine(strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(s))
	if rs := []rune(s); len(rs) > maxReasonLen {
		s = string(rs[:maxReasonLen])
	}
	return s
}
