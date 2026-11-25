package forge

// TokenIntrospection reports what a Box token probe learned. Introspectable is
// false when the hook cannot determine the token's grants. WriteCapable is
// meaningful only when Introspectable is true. It lives here, not in
// backendkit, so a Connector package can return it without importing the
// package that imports the Connector runtime.
type TokenIntrospection struct {
	Introspectable bool
	WriteCapable   bool
	// Reason is an optional operator-facing explanation shown when
	// Introspectable is false.
	Reason string
	// Scopes are the OAuth scopes the forge reports for the token, nil when it
	// reports none.
	Scopes []string
}
