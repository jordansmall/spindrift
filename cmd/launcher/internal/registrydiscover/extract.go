// Package registrydiscover extracts registry declarations from a Target
// repo's own committed config files -- the same files
// cmd/launcher/internal/bindregistry's in-tree rewrite substitutes, named by
// ecosystem.Table's InTreeConfigPath field (ADR 0044/0045) -- so
// `spindrift registry discover` can propose a routes
// file from what the repo already declares, rather than an operator
// transcribing hosts and upstream URLs by hand.
package registrydiscover

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"spindrift.dev/launcher/internal/ecosystem"
)

// Declared is one registry declaration extracted from a committed config
// file.
type Declared struct {
	Ecosystem         string // "cargo" | "npm" | "yarn" | "pnpm"
	ConfigPath        string // repo-relative path it came from
	Host              string // url.URL.Host (hostname, plus ":port" if present)
	UpstreamBaseURL   string // absolute http(s) URL, trailing "/" trimmed
	CargoRegistryName string // cargo only, else ""
}

// Note is a per-config-file observation for the report: the file exists but
// yields no Declared row -- worth surfacing to the operator all the same.
// Skipped distinguishes *why*: false means the file
// genuinely names no registry at all; true means it named one or more
// registries but every one was unusable (non-http(s), userinfo, or an
// unparseable URL) -- a materially different situation an operator must not
// mistake for "nothing declared".
type Note struct {
	ConfigPath string
	Ecosystem  string
	Skipped    bool
}

// parseFunc reads and parses one ecosystem's in-tree config file, given
// repoDir and the path its ecosystem.Table row names.
type parseFunc func(repoDir, configPath string) ([]Declared, *Note, error)

// extractors maps an ecosystem.Table row's Name to the parser that knows its
// in-tree config file's format. The *path* comes from the table, the
// *format* stays here, so neither can drift from the other (issue #3184).
// TestExtractors_MatchInTreeRows keeps the two key sets in step.
var extractors = map[string]parseFunc{
	"cargo": extractCargo,
	"npm":   extractNpm,
	"yarn":  extractYarn,
	"pnpm":  extractPnpm,
}

// Extract scans repoDir for every ecosystem.Table row with a non-empty
// InTreeConfigPath and returns every registry declaration found plus a Note
// for each config file that is present but yields no Declared row -- either
// because it names no registry at all, or because it names one or more but
// every one was unusable (Note.Skipped distinguishes the two). A missing
// config file produces nothing for it. A row with no entry in extractors is
// skipped, not an error -- a future in-tree row acquiring a path before its
// parser lands should discover nothing rather than fail the whole scan.
// Order is deterministic: ecosystem.Table order, then declaration order
// within a file (cargo's TOML map has no source order, so its registry
// names are sorted).
func Extract(repoDir string) ([]Declared, []Note, error) {
	var declared []Declared
	var notes []Note

	for _, row := range ecosystem.Table {
		if row.InTreeConfigPath == "" {
			continue
		}
		parse, ok := extractors[row.Name]
		if !ok {
			continue
		}
		rowDeclared, rowNote, err := parse(repoDir, row.InTreeConfigPath)
		if err != nil {
			return nil, nil, err
		}
		declared = append(declared, rowDeclared...)
		if rowNote != nil {
			notes = append(notes, *rowNote)
		}
	}

	return declared, notes, nil
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
// u.Host != "" check would miss -- and that this package's own hostOnly
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

// rawCargoConfig is the strict decode shape for the slice of
// .cargo/config.toml this package cares about -- only the [registries.*]
// table, which is the only part the in-tree rewrite (and this extractor)
// touches. Unlike registryroutes.Parse, this does not
// DisallowUnknownFields: a real Cargo config carries many other tables
// ([source], [net], [build], ...) that are none of this package's business.
type rawCargoConfig struct {
	Registries map[string]struct {
		Index string `toml:"index"`
	} `toml:"registries"`
}

// extractCargo parses configPath (a .cargo/config.toml) [registries.<name>]
// entries. An index URL's leading "sparse+" is stripped to recover the plain
// upstream base URL -- sparse+ is cargo's own scheme prefix marking the
// sparse protocol (RFC 2789/cargo's registry-index spec), not part of the
// URL the registry-proxy Forwarder or credential lookup ever sees.
func extractCargo(repoDir, configPath string) ([]Declared, *Note, error) {
	data, err := os.ReadFile(filepath.Join(repoDir, configPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("registrydiscover: reading %s: %w", configPath, err)
	}

	var raw rawCargoConfig
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("registrydiscover: parsing %s: %w", configPath, err)
	}

	// Map iteration order is randomized; sort registry names so output is
	// deterministic across runs given the same input file.
	names := make([]string, 0, len(raw.Registries))
	for name := range raw.Registries {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []Declared
	for _, name := range names {
		index := raw.Registries[name].Index
		stripped := strings.TrimPrefix(index, "sparse+")
		host, upstreamBaseURL, ok := httpAbsoluteURL(stripped)
		if !ok {
			// Not an absolute http(s) URL after stripping -- skip this entry
			// rather than erroring the whole file; only the file's own TOML
			// syntax is an error (checked above).
			continue
		}
		// names iterates raw.Registries' own keys, so each name is already
		// unique -- no dedup needed here (unlike the line-scanning
		// extractors, which can see the same URL declared twice).
		out = append(out, Declared{
			Ecosystem:         "cargo",
			ConfigPath:        configPath,
			Host:              host,
			UpstreamBaseURL:   upstreamBaseURL,
			CargoRegistryName: name,
		})
	}

	if len(out) == 0 {
		// len(names) > 0 means the file named one or more [registries.*]
		// tables but every index was unusable -- distinct from a file that
		// names no registry at all.
		return nil, &Note{ConfigPath: configPath, Ecosystem: "cargo", Skipped: len(names) > 0}, nil
	}
	return out, nil, nil
}

// extractNpm scans configPath (a .npmrc) line-by-line for "registry=<url>"
// and "@scope:registry=<url>" entries (any scope name). .npmrc is npm's own
// ini-ish format, not TOML or YAML, and npm accepts values with no quoting
// at all -- a line-based scan matches what npm itself parses far more
// closely than reaching for a general-purpose ini library would for this
// one line shape.
func extractNpm(repoDir, configPath string) ([]Declared, *Note, error) {
	data, err := os.ReadFile(filepath.Join(repoDir, configPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("registrydiscover: reading %s: %w", configPath, err)
	}

	seenURL := make(map[string]bool)
	var out []Declared
	sawDeclaration := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "registry" && !strings.HasSuffix(key, ":registry") {
			continue
		}
		sawDeclaration = true
		host, upstreamBaseURL, ok := httpAbsoluteURL(value)
		if !ok || seenURL[upstreamBaseURL] {
			continue
		}
		seenURL[upstreamBaseURL] = true
		out = append(out, Declared{
			Ecosystem:       "npm",
			ConfigPath:      configPath,
			Host:            host,
			UpstreamBaseURL: upstreamBaseURL,
		})
	}

	if len(out) == 0 {
		// sawDeclaration means the file named a registry key but its value
		// (or every one, if repeated) was unusable -- distinct from a file
		// that names no registry key at all.
		return nil, &Note{ConfigPath: configPath, Ecosystem: "npm", Skipped: sawDeclaration}, nil
	}
	return out, nil, nil
}

// extractYarn scans configPath (a .yarnrc.yml) line-by-line for
// "npmRegistryServer: <url>" keys -- both the top-level default and any
// nested under npmScopes -- since the repo has no YAML dependency and must
// not add one (see package doc): a line-based scan of this one known key,
// ignoring indentation/nesting structure entirely, covers the shapes yarn
// berry actually emits without pulling in a full YAML parser for a single
// key.
func extractYarn(repoDir, configPath string) ([]Declared, *Note, error) {
	data, err := os.ReadFile(filepath.Join(repoDir, configPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("registrydiscover: reading %s: %w", configPath, err)
	}

	seenURL := make(map[string]bool)
	var out []Declared
	sawDeclaration := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			// A full-line comment (yarn berry's .yarnrc.yml has no other
			// comment syntax) -- must not be mistaken for a declaration,
			// same as extractNpm's "#"/";" line filter.
			continue
		}
		key, value, ok := splitYAMLKeyValue(trimmed)
		if !ok || key != "npmRegistryServer" {
			continue
		}
		sawDeclaration = true
		value = unquoteYAMLScalar(stripYAMLTrailingComment(value))
		host, upstreamBaseURL, ok := httpAbsoluteURL(value)
		if !ok || seenURL[upstreamBaseURL] {
			continue
		}
		seenURL[upstreamBaseURL] = true
		out = append(out, Declared{
			Ecosystem:       "yarn",
			ConfigPath:      configPath,
			Host:            host,
			UpstreamBaseURL: upstreamBaseURL,
		})
	}

	if len(out) == 0 {
		// sawDeclaration means npmRegistryServer was set but every value was
		// unusable -- distinct from a file that never sets the key at all.
		return nil, &Note{ConfigPath: configPath, Ecosystem: "yarn", Skipped: sawDeclaration}, nil
	}
	return out, nil, nil
}

// isPnpmRegistryKey reports whether key is a real pnpm registry key: the
// bare top-level "registry", or a scoped catalog key "<scope>:registry"
// where scope starts with "@" (a quoted key like "\"@myorg:registry\""
// arrives here already unquoted by splitYAMLKeyValue). A suffix-only check
// would also match an unrelated key like "myregistry" or a YAML list item
// key like "- registry".
func isPnpmRegistryKey(key string) bool {
	if key == "registry" {
		return true
	}
	return strings.HasPrefix(key, "@") && strings.HasSuffix(key, ":registry")
}

// extractPnpm scans configPath (a pnpm-workspace.yaml) line-by-line for the
// bare "registry:" key or a scoped catalog key like "\"@myorg:registry\":"
// with an http(s) value -- same line-based approach as extractYarn and for
// the same reason (no YAML dependency, see package doc). isPnpmRegistryKey
// does the exact match, so an unrelated key merely ending in "registry"
// (e.g. "myregistry") or a YAML list item ("- registry: ...") is never
// mistaken for a real pnpm registry declaration.
func extractPnpm(repoDir, configPath string) ([]Declared, *Note, error) {
	data, err := os.ReadFile(filepath.Join(repoDir, configPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("registrydiscover: reading %s: %w", configPath, err)
	}

	seenURL := make(map[string]bool)
	var out []Declared
	sawDeclaration := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			// A full-line comment -- skip it outright rather than feeding it
			// to splitYAMLKeyValue, which has no notion of "#" as special.
			continue
		}
		key, value, ok := splitYAMLKeyValue(trimmed)
		if !ok || !isPnpmRegistryKey(key) {
			continue
		}
		sawDeclaration = true
		value = unquoteYAMLScalar(stripYAMLTrailingComment(value))
		host, upstreamBaseURL, ok := httpAbsoluteURL(value)
		if !ok || seenURL[upstreamBaseURL] {
			continue
		}
		seenURL[upstreamBaseURL] = true
		out = append(out, Declared{
			Ecosystem:       "pnpm",
			ConfigPath:      configPath,
			Host:            host,
			UpstreamBaseURL: upstreamBaseURL,
		})
	}

	if len(out) == 0 {
		// sawDeclaration means a real registry key (per isPnpmRegistryKey)
		// was set but every value was unusable -- distinct from a file with
		// no such key at all.
		return nil, &Note{ConfigPath: configPath, Ecosystem: "pnpm", Skipped: sawDeclaration}, nil
	}
	return out, nil, nil
}
