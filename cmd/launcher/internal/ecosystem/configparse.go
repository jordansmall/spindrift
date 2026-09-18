package ecosystem

// This file holds the parsing helpers shared by more than one row's
// ConfigParser. A helper used by only one ecosystem stays in that ecosystem's
// own file instead.

import (
	"net/url"
	"strings"
)

// httpAbsoluteURL reports raw's host and trimmed base URL when raw is an
// absolute http(s) URL with no userinfo. Rejecting userinfo matches
// registryroutes.ValidateUpstreamOrigin and keeps a credential out of the
// routes file. u.Hostname() rejects a port-only host ("http://:8080/"), which
// registryvocab.HostKey normalizes to the empty match-host that Parse rejects.
func httpAbsoluteURL(raw string) (host, upstreamBaseURL string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return "", "", false
	}
	return u.Host, strings.TrimSuffix(raw, "/"), true
}

// splitYAMLKeyValue splits a trimmed YAML mapping line into its key and value.
// A quoted key can hold a ":" itself (pnpm-workspace.yaml writes scoped
// catalog entries as `"@myorg:registry": <url>`), so the split point is the
// ":" after the closing quote. Cutting on the line's first ":" would slice
// such a key in half.
func splitYAMLKeyValue(trimmed string) (key, value string, ok bool) {
	if len(trimmed) > 0 && (trimmed[0] == '"' || trimmed[0] == '\'') {
		q := trimmed[0]
		end := strings.IndexByte(trimmed[1:], q)
		if end < 0 {
			return "", "", false
		}
		end++ // index of the closing quote within trimmed
		key = trimmed[1:end]
		rest := strings.TrimSpace(trimmed[end+1:])
		rest, ok = strings.CutPrefix(rest, ":")
		if !ok {
			return "", "", false
		}
		return key, strings.TrimSpace(rest), true
	}
	k, v, ok := strings.Cut(trimmed, ":")
	if !ok {
		return "", "", false
	}
	return strings.TrimSpace(k), strings.TrimSpace(v), true
}

// unquoteYAMLScalar strips one matching pair of enclosing quotes from a YAML
// scalar. That is the only quoting shape this package's line-based scan has to
// undo, since registry values are plain URLs with no escape sequences.
func unquoteYAMLScalar(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// stripYAMLTrailingComment removes a trailing YAML comment from a value
// already split off a "key: value" line, so `npmRegistryServer:
// https://x.example.com # our mirror` keeps the URL. A "#" inside quotes
// survives. An unquoted value is cut only at a "#" that whitespace precedes:
// a bare URL holds no whitespace, so a "#" with none before it is a fragment.
func stripYAMLTrailingComment(value string) string {
	if len(value) > 0 && (value[0] == '"' || value[0] == '\'') {
		end := strings.IndexByte(value[1:], value[0])
		if end < 0 {
			return value
		}
		return value[:end+2] // include the opening and closing quote
	}
	for i := 1; i < len(value); i++ {
		if value[i] == '#' && (value[i-1] == ' ' || value[i-1] == '\t') {
			return strings.TrimSpace(value[:i])
		}
	}
	return value
}
