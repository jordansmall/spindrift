package connectorforge

import (
	"context"
	"errors"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
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
	return introspectToken(classedCall(c, retry.Clock{}), c.Capabilities, t, token)
}

func introspectToken(call guestCall, caps []string, t Target, token string) (backendkit.TokenIntrospection, error) {
	if !slices.Contains(caps, capTokenGrant) {
		return backendkit.TokenIntrospection{}, ErrNoTokenGrant
	}
	p := struct {
		Target      Target `json:"target"`
		TokenPrefix string `json:"tokenPrefix"`
	}{t, tokenPrefix(token)}
	var g wireGrant
	if err := call(context.Background(), "forge.tokenGrant", p, &g); err != nil {
		return backendkit.TokenIntrospection{}, err
	}
	return backendkit.TokenIntrospection(g), nil
}
