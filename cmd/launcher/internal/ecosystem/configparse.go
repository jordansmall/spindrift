package ecosystem

// This file holds the parsing helpers shared by more than one row's
// ConfigParser: httpAbsoluteURL (cargo, npm, yarn, pnpm) and the
// YAML-line-scanning trio splitYAMLKeyValue/unquoteYAMLScalar/
// stripYAMLTrailingComment (yarn, pnpm). A helper used by only one
// ecosystem stays in that ecosystem's own file instead (e.g. cargo.go's
// rawCargoConfig, pnpm.go's isPnpmRegistryKey).

import (
	"net/url"
	"strings"
)

// httpAbsoluteURL parses raw and reports its (host, trailing-slash-trimmed
// base URL) if it is an absolute http(s) URL with no userinfo, mirroring
// registryroutes.ValidateUpstreamOrigin's userinfo rejection so that a
// credential embedded in a config's registry URL (e.g. .npmrc's
// "registry=https://user:pass@host/") never gets copied verbatim into the
// generated routes file. ok is false for anything else (relative, malformed,
// non-http(s), userinfo, port-only e.g. "http://:8080/") -- the caller skips
// that declaration rather than erroring, since a config file naming an
// unusable value is still a valid file, just not a route source.
// u.Hostname() strips the port, so it catches a port-only host ("http://
// :8080/" parses to u.Host == ":8080" but u.Hostname() == "") that a bare
// u.Host != "" check would miss -- and that registryvocab.HostKey
// would otherwise normalize to "", the empty match-host
// registryroutes.Parse rejects (registrydiscover.go's never-write-what-
// Parse-would-reject invariant).
func httpAbsoluteURL(raw string) (host, upstreamBaseURL string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return "", "", false
	}
	return u.Host, strings.TrimSuffix(raw, "/"), true
}

// splitYAMLKeyValue splits a single trimmed YAML mapping line into its key
// and value. A quoted key (pnpm-workspace.yaml's scoped catalog entries look
// like `"@myorg:registry": <url>`) is handled specially: the key itself can
// contain a ":" (the scope separator), so the split point is the matching
// close-quote's following ":", not the line's first ":" -- a plain
// strings.Cut on the first ":" would slice the quoted key in half. An
// unquoted key (yarn's bare "npmRegistryServer:") has no such colon, so
// splitting on the first ":" is exact for that shape.
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

// unquoteYAMLScalar strips a single matching pair of enclosing quotes (' or
// ") from a YAML scalar value, if present -- the only quoting shape this
// package's line-based scan needs to undo, since npmRegistryServer/registry
// values are plain URLs with no escape sequences of their own to unescape.
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
// https://x.example.com # our mirror` keeps the URL instead of losing it to
// a failed URL parse. A quoted value (leading ' or ") is trusted as-is up to
// its closing quote -- anything after that quote is the comment, but a "#"
// *inside* the quotes (e.g. a URL fragment) is left untouched since it was
// never inspected. An unquoted value is truncated at the first
// whitespace-then-"#": a bare URL cannot itself contain whitespace, so a
// space or tab immediately before "#" unambiguously marks a comment (YAML
// permits either as the separator), while a bare "#" with no preceding
// whitespace (a URL fragment) is left alone.
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
