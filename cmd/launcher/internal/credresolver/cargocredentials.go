package credresolver

import (
	"fmt"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// cargoCredentialsFile is the TOML decode target for a credentials.toml
// file. Token is a plain string: an absent "token" key and an explicitly
// empty "token = \"\"" both decode to "", and the two are never told apart
// (go-toml/v2 gives no signal to distinguish them, and nothing downstream
// needs to) -- both hit the same "but no token field" branch below.
type cargoCredentialsFile struct {
	Registries map[string]struct {
		Token string `toml:"token"`
	} `toml:"registries"`
}

// cargoCredentialsToken parses content as a cargo credentials.toml file and
// returns the token of the "[registries.NAME]" table whose NAME exactly
// equals registryName. Pure: does no I/O itself -- callers own reading the
// file; sourceName is used only to name the source in the returned error,
// never logged or echoed alongside a credential value. When no table
// matches registryName, or a matching table has no "token" key (or an empty
// one), this returns an error rather than an empty string with a nil error
// -- a proxy that goes on to run unauthenticated because of a silent miss is
// the failure mode this guards against.
func cargoCredentialsToken(content []byte, sourceName, registryName string) (string, error) {
	var parsed cargoCredentialsFile
	if err := toml.Unmarshal(content, &parsed); err != nil {
		return "", fmt.Errorf("parsing cargo credentials file %s: %w", sourceName, err)
	}

	entry, ok := parsed.Registries[registryName]
	if !ok {
		return "", fmt.Errorf("cargo credentials file %s has no [registries.%s] table", sourceName, registryName)
	}
	if entry.Token == "" {
		// An empty quoted string ("" or '') must not count as found --
		// unlike netrc.go's strings.Fields(), TOML makes an empty value
		// representable, so this must fail closed explicitly.
		return "", fmt.Errorf("cargo credentials file %s has table [registries.%s] but no token field", sourceName, registryName)
	}
	if hasDisallowedTokenChars(entry.Token) {
		// Fails closed on quote/backslash/control characters: each either
		// indicates a value shape the old hand-rolled scanner never
		// accepted (e.g. a triple-quoted string, impossible for it to
		// produce), or cannot travel in an HTTP header value as-is (CR,
		// LF, tab, NUL, ...) -- go-toml/v2's escape decoding can produce
		// these even though the old scanner could not.
		return "", fmt.Errorf("cargo credentials file %s has table [registries.%s] but its token contains a quote, backslash, or control character", sourceName, registryName)
	}
	return entry.Token, nil
}

// hasDisallowedTokenChars reports whether token contains a quote, a
// backslash, or any control character (runes below 0x20, plus DEL) --
// characters cargoCredentialsToken refuses to resolve as a credential.
func hasDisallowedTokenChars(token string) bool {
	if strings.ContainsAny(token, "\"'\\") {
		return true
	}
	for _, r := range token {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
