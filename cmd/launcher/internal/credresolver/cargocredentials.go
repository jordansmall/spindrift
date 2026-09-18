package credresolver

import (
	"fmt"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// cargoCredentialsFile is the TOML decode target for a credentials.toml file.
// An absent "token" key and an explicit empty one both decode to "", and
// go-toml/v2 gives no signal to tell them apart, so both take the same
// "no token field" branch below.
type cargoCredentialsFile struct {
	Registries map[string]struct {
		Token string `toml:"token"`
	} `toml:"registries"`
}

// cargoCredentialsToken returns the token of the "[registries.NAME]" table
// whose NAME exactly equals registryName. It does no I/O, and sourceName only
// names the source in the returned error, never alongside a credential value.
// A miss returns an error rather than an empty string with a nil error, so a
// proxy cannot silently go on to run unauthenticated.
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
		// Unlike netrc.go's strings.Fields(), TOML can represent an empty
		// value, so an empty quoted string has to fail closed here.
		return "", fmt.Errorf("cargo credentials file %s has table [registries.%s] but no token field", sourceName, registryName)
	}
	if hasDisallowedTokenChars(entry.Token) {
		// go-toml/v2's escape decoding can yield characters that cannot
		// travel in an HTTP header value as-is (CR, LF, tab, NUL).
		return "", fmt.Errorf("cargo credentials file %s has table [registries.%s] but its token contains a quote, backslash, or control character", sourceName, registryName)
	}
	return entry.Token, nil
}

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
