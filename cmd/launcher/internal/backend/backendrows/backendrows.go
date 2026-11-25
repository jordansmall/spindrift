// Package backendrows applies the backend rows of the launcher input document
// (ADR 0073, issue #6805): a network Connector's compiled module, Route table
// and credential knob. A backend without a row here is built from the generated
// registry. It is a package of its own because it imports connectorhost, which
// must not import the registry.
package backendrows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
	"sync"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorrecord"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/registryvocab"
)

// Row is one backend row of the input document.
type Row struct {
	Name string `json:"name"`
	// Module is the path of the Connector's compiled wasm.
	Module string `json:"module"`
	// Routes is the only traffic the Connector may send.
	Routes []connectorhost.Route `json:"routes"`
	// CredentialEnv names the host env var holding the secret the host attaches
	// to each admitted request, as a bearer token or, under CredentialScheme
	// "basic", as the Basic password. The Connector never sees it.
	CredentialEnv string `json:"credentialEnv"`
	// CredentialScheme is how CredentialEnv is attached: "" or "bearer" sends
	// it as a Bearer token, "basic" pairs it with IdentityEnv's value as Basic
	// credentials (Jira Cloud: an account email and an API token).
	CredentialScheme string `json:"credentialScheme,omitempty"`
	// IdentityEnv names the host env var holding the Basic identity. It is
	// required under the "basic" scheme and refused otherwise.
	IdentityEnv string `json:"identityEnv,omitempty"`
	// CredentialKnob is the Consumer knob that names CredentialEnv; doctor shows it.
	CredentialKnob string `json:"credentialKnob,omitempty"`
	// AcceptedRoutes is the Consumer's accepted baseline of Routes, which doctor
	// diffs Routes against. Unlike Routes it may be empty or absent: nothing
	// accepted yet, so every live route counts as added.
	AcceptedRoutes []connectorhost.Route `json:"acceptedRoutes,omitempty"`
	// AcceptedGrant is the Consumer's accepted baseline of the non-Route parts of
	// the grant. nil means the entry recorded Routes only (a list entry, or no
	// entry), so GrantChanges compares nothing.
	AcceptedGrant *Grant `json:"acceptedGrant,omitempty"`
	// BaseEnv names the knob env var holding the site's https base URL, for a
	// Connector whose host is the Consumer's own (a Jira site). A Route or
	// accepted Route whose Host is BaseHost takes the base URL's host and has
	// the base URL's path prefix prepended to its Path; Use resolves them, so a
	// stored Row carries concrete hosts.
	BaseEnv string `json:"baseEnv,omitempty"`
	// GitRemote is the forge's https web base that core derives the clone URL
	// <GitRemote>/<owner>/<name>.git from. A row sets it literally, as an origin
	// with no path, or names the knob in GitRemoteEnv; Use resolves the latter
	// into it, so a stored Row carries the concrete base. A row with neither
	// names no Git remote (a tracker); the forge code refuses it.
	GitRemote string `json:"gitRemote,omitempty"`
	// GitRemoteEnv names the knob env var holding the forge's web base URL,
	// which may carry a path prefix (a Forgejo served under a subpath).
	GitRemoteEnv string `json:"gitRemoteEnv,omitempty"`
}

// Grant is the accepted non-Route parts of a Row's grant, named as the Row names
// them. A part compared verbatim (every one but the scheme, which normalizes "" to
// bearer) is accepted as absent when the Grant lacks it, so a Row declaring it is
// a widening; a Row declaring less than the Grant accepts is a narrowing, and silent.
type Grant struct {
	CredentialEnv    string `json:"credentialEnv,omitempty"`
	CredentialScheme string `json:"credentialScheme,omitempty"`
	BaseEnv          string `json:"baseEnv,omitempty"`
	GitRemote        string `json:"gitRemote,omitempty"`
	GitRemoteEnv     string `json:"gitRemoteEnv,omitempty"`
}

// GrantChange is one non-Route part of a Row's grant that differs from the
// accepted baseline. Declared and Accepted render an absent value as "none".
type GrantChange struct {
	Part, Declared, Accepted string
}

// BaseHost is the Route host that stands for the host of the row's BaseEnv URL.
const BaseHost = "{base}"

// The CredentialScheme values a row may name; "" means SchemeBearer.
const (
	SchemeBearer = "bearer"
	SchemeBasic  = "basic"
)

// Widening returns the live Routes no accepted Route covers. An accepted Route
// covers one with the same method and host, compared literally, no redirect
// opt-in the accepted one lacks, and a path equal to or inside the accepted
// path's subtree. The subtree test is PathSet.Admits' request-path match,
// sound here because Use validates both tables as canonical patterns (no
// blanket "/").
func (r Row) Widening() []connectorhost.Route {
	var added []connectorhost.Route
	for _, live := range r.Routes {
		covered := slices.ContainsFunc(r.AcceptedRoutes, func(a connectorhost.Route) bool {
			return a.Method == live.Method && a.Host == live.Host && (a.FollowRedirect || !live.FollowRedirect) &&
				registryvocab.PathSet{a.Path}.Admits(live.Path)
		})
		if !covered {
			added = append(added, live)
		}
	}
	return added
}

// GrantChanges returns the non-Route parts of the row's grant that differ from
// AcceptedGrant, nil when AcceptedGrant is nil. A part the row declares absent
// is skipped, as Widening is silent on a narrowed Route. The authorization
// scheme compares normalized ("" is "bearer"). The Git remote compares the
// declaration: Use resolves GitRemoteEnv into GitRemote, so a row naming the
// env is compared by that name alone, never by the resolved value.
func (r Row) GrantChanges() []GrantChange {
	a := r.AcceptedGrant
	if a == nil {
		return nil
	}
	var out []GrantChange
	add := func(part, declared, accepted string) {
		if declared != "" && declared != accepted {
			out = append(out, GrantChange{part, orNone(declared), orNone(accepted)})
		}
	}
	add("credential variable", r.CredentialEnv, a.CredentialEnv)
	add("authorization scheme", schemeOf(r.CredentialScheme), schemeOf(a.CredentialScheme))
	add("base", r.BaseEnv, a.BaseEnv)
	add("Git remote", remoteDecl(r.GitRemote, r.GitRemoteEnv), remoteDecl(a.GitRemote, a.GitRemoteEnv))
	return out
}

func schemeOf(s string) string {
	if s == "" {
		return SchemeBearer
	}
	return s
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// remoteDecl renders a Git remote declaration; the env name wins over the
// resolved literal Use stored beside it.
func remoteDecl(literal, env string) string {
	if env != "" {
		return "env " + env
	}
	return literal
}

// coreBackends are the built-in backends a row may not replace. It mirrors the
// `core = true` rows of lib/backends/ (ADR 0068, decision 7).
var coreBackends = map[string]bool{"git": true, "local": true}

var (
	mu      sync.RWMutex
	applied = map[string]Row{}
	// unusableBase holds the base error of each row Use dropped.
	unusableBase = map[string]error{}
)

// Use applies the rows in raw, a JSON array of Row, replacing any rows an
// earlier call applied; empty raw clears them. A row with no name, a repeated
// or built-in name, no module, credentialEnv or routes, or an invalid Route
// table or non-empty accepted baseline is an error. A row's BaseHost routes
// are resolved from lookup(BaseEnv) first, so validation sees the concrete
// table (an unusable base drops the row, see resolveBase); BaseHost without
// a BaseEnv is an error. The credential is only required at request time (see
// Load). Rows are validated whole before any is stored.
func Use(raw json.RawMessage, lookup func(string) string) error {
	var rows []Row
	if len(bytes.TrimSpace(raw)) != 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&rows); err != nil {
			return fmt.Errorf("backend rows: %w", err)
		}
	}
	seen := map[string]bool{}
	next := make(map[string]Row, len(rows))
	dropped := map[string]error{}
	for _, row := range rows {
		resolved, unusable, err := resolveBase(row, lookup)
		if err == nil {
			var remoteUnusable error
			if resolved, remoteUnusable, err = resolveGitRemote(resolved, lookup); unusable == nil {
				unusable = remoteUnusable
			}
		}
		if err == nil {
			err = validate(resolved, seen)
		}
		if err != nil {
			return fmt.Errorf("backend rows: %w", err)
		}
		if unusable != nil {
			dropped[resolved.Name] = unusable
		} else if gateAllows(resolved.Name, lookup) {
			next[resolved.Name] = resolved
		}
	}
	mu.Lock()
	defer mu.Unlock()
	applied, unusableBase = next, dropped
	return nil
}

var gates = map[string]func(lookup func(string) string) bool{}

// RegisterGate has Use drop, after validating it, the row named name whenever
// applies answers false for the run's settings: a backend whose Routes cannot
// serve the settings gets no row, so no later reader sees one. Call it from
// init.
func RegisterGate(name string, applies func(lookup func(string) string) bool) {
	mu.Lock()
	defer mu.Unlock()
	gates[name] = applies
}

func gateAllows(name string, lookup func(string) string) bool {
	mu.RLock()
	applies, ok := gates[name]
	mu.RUnlock()
	return !ok || applies(lookup)
}

// unsetHost is the placeholder base host an unusable row resolves against, so
// its routes still validate whole before it is dropped.
const unsetHost = "unset.invalid"

// resolveBase returns row with every BaseHost route host and path prefix
// replaced from the base URL its BaseEnv names, so ValidateRoutes judges the
// table the Connector will actually be held to. A base is unusable when
// BaseEnv is unset or names anything but a bare https URL. Then unusable
// carries that error and the caller drops the row, since a Consumer importing
// every Connector need not configure a tracker it does not use.
func resolveBase(row Row, lookup func(string) string) (_ Row, unusable, _ error) {
	uses := func(routes []connectorhost.Route) bool {
		return slices.ContainsFunc(routes, func(r connectorhost.Route) bool { return r.Host == BaseHost })
	}
	if !uses(row.Routes) && !uses(row.AcceptedRoutes) {
		return row, nil, nil
	}
	if row.BaseEnv == "" {
		return row, nil, fmt.Errorf("backend %q: a route host is %s but the row has no baseEnv", row.Name, BaseHost)
	}
	u, prefix, err := parseWebBase(row.Name, row.BaseEnv, lookup(row.BaseEnv))
	if err != nil {
		u, prefix, unusable = &url.URL{Scheme: "https", Host: unsetHost}, "", err
	}
	host := u.Host
	apply := func(routes []connectorhost.Route) []connectorhost.Route {
		out := slices.Clone(routes)
		for i := range out {
			if out[i].Host == BaseHost {
				out[i].Host = host
				// "/" is the base's own subtree root: prefix+"/" is not canonical.
				if out[i].Path == "/" && prefix != "" {
					out[i].Path = prefix
				} else {
					out[i].Path = prefix + out[i].Path
				}
			}
		}
		return out
	}
	row.Routes = apply(row.Routes)
	row.AcceptedRoutes = apply(row.AcceptedRoutes)
	return row, unusable, nil
}

// parseWebBase judges raw, the value of the knob env, as a web base URL: bare
// https, no userinfo, escaped path, query or fragment, with a canonical path
// prefix (returned without its trailing "/").
func parseWebBase(name, env, raw string) (*url.URL, string, error) {
	u, perr := url.Parse(raw)
	if perr != nil {
		u = &url.URL{} // url.Parse returns nil on error
	}
	prefix := strings.TrimSuffix(u.Path, "/")
	var err error
	switch {
	case raw == "":
		err = fmt.Errorf("backend %q: %s is not set", name, env)
	case perr != nil:
		err = fmt.Errorf("backend %q: %s is not a URL", name, env)
	case u.Scheme != "https":
		err = fmt.Errorf("backend %q: %s must be an https URL", name, env)
	case u.User != nil:
		err = fmt.Errorf("backend %q: %s must not carry userinfo", name, env)
	case u.RawPath != "":
		err = fmt.Errorf("backend %q: %s must not carry an escaped path", name, env)
	case strings.ContainsAny(raw, "?#"):
		err = fmt.Errorf("backend %q: %s must not carry a query or fragment", name, env)
	case !bareHost(u):
		err = fmt.Errorf("backend %q: %s must name a host", name, env)
	case !canonicalPrefix(prefix):
		err = fmt.Errorf("backend %q: %s must have a canonical path", name, env)
	}
	return u, prefix, err
}

// resolveGitRemote returns row with GitRemote set from lookup(GitRemoteEnv),
// its trailing "/" trimmed. An unset or malformed knob value makes the row
// unusable, dropped like an unusable base. A literal GitRemote that is not a
// bare https origin, or a row carrying both fields, is an error.
func resolveGitRemote(row Row, lookup func(string) string) (_ Row, unusable, _ error) {
	switch {
	case row.GitRemote != "" && row.GitRemoteEnv != "":
		return row, nil, fmt.Errorf("backend %q: gitRemote and gitRemoteEnv are mutually exclusive", row.Name)
	case row.GitRemote != "":
		u, _, err := parseWebBase(row.Name, "gitRemote", row.GitRemote)
		if err == nil && u.Path != "" {
			err = fmt.Errorf("backend %q: gitRemote must be an origin with no path", row.Name)
		}
		return row, nil, err
	case row.GitRemoteEnv != "":
		raw := lookup(row.GitRemoteEnv)
		if _, _, err := parseWebBase(row.Name, row.GitRemoteEnv, raw); err != nil {
			return row, err, nil
		}
		row.GitRemote = strings.TrimSuffix(raw, "/")
	}
	return row, nil, nil
}

// bareHost reports whether u names a host[:port] that Route.validate would
// accept: url.Parse unescapes an IPv6 zone ("%25en0") into a host that no
// longer re-parses to itself.
func bareHost(u *url.URL) bool {
	r, err := url.Parse("https://" + u.Host)
	return err == nil && r.Host == u.Host && u.Hostname() != "" && !strings.HasSuffix(u.Host, ":")
}

// canonicalPrefix reports whether a base path prefix joins onto a route path to
// a canonical path: "" or an absolute path with no empty or dot segments. "/" is
// not, since prefix+"/rest" is "//rest".
func canonicalPrefix(prefix string) bool {
	return prefix == "" || prefix != "/" && path.Clean(prefix) == prefix
}

func validScheme(s string) bool {
	return s == "" || s == SchemeBearer || s == SchemeBasic
}

func schemeError(s string) error {
	return fmt.Errorf("unknown credentialScheme %q, want bearer or basic", s)
}

func validate(row Row, seen map[string]bool) error {
	switch {
	case row.Name == "":
		return errors.New("a backend row has no name")
	case seen[row.Name]:
		return fmt.Errorf("backend %q has more than one row", row.Name)
	case coreBackends[row.Name]:
		return fmt.Errorf("backend %q is built in, so a row cannot replace it", row.Name)
	case row.Module == "":
		return fmt.Errorf("backend %q has no module", row.Name)
	case row.CredentialEnv == "":
		return fmt.Errorf("backend %q has no credentialEnv", row.Name)
	case len(row.Routes) == 0:
		return fmt.Errorf("backend %q has an empty route table", row.Name)
	case !validScheme(row.CredentialScheme):
		return fmt.Errorf("backend %q: %w", row.Name, schemeError(row.CredentialScheme))
	case row.CredentialScheme == SchemeBasic && row.IdentityEnv == "":
		return fmt.Errorf("backend %q: the basic credentialScheme needs an identityEnv", row.Name)
	case row.CredentialScheme != SchemeBasic && row.IdentityEnv != "":
		return fmt.Errorf("backend %q: identityEnv applies only to the basic credentialScheme", row.Name)
	case row.AcceptedGrant != nil && !validScheme(row.AcceptedGrant.CredentialScheme):
		return fmt.Errorf("backend %q: accepted grant: %w", row.Name, schemeError(row.AcceptedGrant.CredentialScheme))
	case row.AcceptedGrant != nil && row.AcceptedGrant.GitRemote != "" && row.AcceptedGrant.GitRemoteEnv != "":
		return fmt.Errorf("backend %q: accepted grant: gitRemote and gitRemoteEnv are mutually exclusive", row.Name)
	}
	seen[row.Name] = true
	if err := connectorhost.ValidateRoutes(row.Routes); err != nil {
		return fmt.Errorf("backend %q: %w", row.Name, err)
	}
	if len(row.AcceptedRoutes) != 0 {
		if err := connectorhost.ValidateRoutes(row.AcceptedRoutes); err != nil {
			return fmt.Errorf("backend %q: accepted routes: %w", row.Name, err)
		}
	}
	return nil
}

// Lookup returns the document's row for the backend name. ok is false when the
// document has none, and the caller builds the native backend from the
// generated registry.
func Lookup(name string) (Row, bool) {
	mu.RLock()
	defer mu.RUnlock()
	row, ok := applied[name]
	return row, ok
}

// All returns every applied row, sorted by name.
func All() []Row {
	mu.RLock()
	defer mu.RUnlock()
	rows := slices.Collect(maps.Values(applied))
	slices.SortFunc(rows, func(a, b Row) int { return strings.Compare(a.Name, b.Name) })
	return rows
}

// Options is what Load takes from its host process.
type Options struct {
	// Getenv resolves the credential; nil means os.Getenv.
	Getenv func(string) string
	// Client is the transport; nil means a default client.
	Client *http.Client
	// Sandbox is where the module loads; nil means the installed one.
	Sandbox connectorsandbox.Loader
}

// Authorizer returns the function that attaches the row's credential to an
// admitted request. It reads getenv at each request, so a rotated token reaches
// the next one, and a missing credential fails the request before it is sent.
func (row Row) Authorizer(getenv func(string) string) func(*http.Request) error {
	return func(r *http.Request) error {
		token := getenv(row.CredentialEnv)
		if token == "" {
			return errors.New(row.CredentialEnv + " is not set")
		}
		if row.CredentialScheme == SchemeBasic {
			identity := getenv(row.IdentityEnv)
			if identity == "" {
				return errors.New(row.IdentityEnv + " is not set")
			}
			r.SetBasicAuth(identity, token)
			return nil
		}
		r.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
}

// Load compiles the row's module as a network Connector bound to its Route
// table. The credential is read at each request, not at load, and a missing one
// fails the request before it is sent.
func Load(ctx context.Context, row Row, o Options) (*connectorhost.Connector, error) {
	// Opening the audit file is part of loading, before the module is read, so
	// a Connector never runs unrecorded.
	audit, err := connectorrecord.AuditFile(row.Name)
	if err != nil {
		return nil, fmt.Errorf("backend %q: %w", row.Name, err)
	}
	getenv := o.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	wasm, err := os.ReadFile(row.Module)
	if err != nil {
		return nil, fmt.Errorf("backend %q: read module: %w", row.Name, err)
	}
	n := connectorhost.Network{
		Name:      row.Name,
		Routes:    row.Routes,
		Authorize: row.Authorizer(getenv),
		Client:    o.Client,
		Audit:     connectorhost.NewAuditLog(audit),
	}
	if o.Sandbox != nil {
		return o.Sandbox.LoadNetwork(ctx, wasm, n)
	}
	return connectorsandbox.LoadNetwork(ctx, wasm, n)
}

// Snapshot captures the applied rows and registered gates and returns the
// function that restores them, for a test that applies rows to the
// process-global set. It is not in export_test.go because cmd/launcher's
// tests need it.
func Snapshot() (restore func()) {
	mu.Lock()
	defer mu.Unlock()
	saved, savedGates, savedBase := maps.Clone(applied), maps.Clone(gates), maps.Clone(unusableBase)
	return func() {
		mu.Lock()
		defer mu.Unlock()
		applied, gates, unusableBase = saved, savedGates, savedBase
	}
}

// BaseError returns why Use dropped the named row, so a caller that finds no
// row can say why. It is nil when the row applied or is absent.
func BaseError(name string) error {
	mu.RLock()
	defer mu.RUnlock()
	return unusableBase[name]
}

// Baseline renders the Route baseline file (spindrift-connector-routes.json)
// from raw, the launcher input document's backend rows: one object entry per
// Connector, keyed by name in sorted order, recording its whole grant. raw
// must be the document's own rows, not Lookup's: Use rewrites {base} hosts in
// the stored rows, and an accepted baseline records the declaration (the {base}
// placeholder and the knob names), never a resolved host. The scheme is always
// written, normalized; base and Git remote only when declared, so an omitted
// part is accepted as absent. Routes keep the row's order, one per line, so a
// change reads as a reviewable diff.
func Baseline(raw json.RawMessage) ([]byte, error) {
	var rows []Row
	if len(bytes.TrimSpace(raw)) != 0 {
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, fmt.Errorf("backend rows: %w", err)
		}
	}
	if len(rows) == 0 {
		return nil, errors.New("no Connector backend rows in the configuration to accept")
	}
	slices.SortFunc(rows, func(a, b Row) int { return strings.Compare(a.Name, b.Name) })
	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, row := range rows {
		if err := writeBaselineEntry(&buf, row); err != nil {
			return nil, err
		}
		if i < len(rows)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("}\n")
	return buf.Bytes(), nil
}

func writeBaselineEntry(buf *bytes.Buffer, row Row) error {
	name, err := marshalPlain(row.Name)
	if err != nil {
		return err
	}
	fmt.Fprintf(buf, "  %s: {\n", name)
	for _, kv := range []struct {
		key, val string
		always   bool
	}{
		{"credentialEnv", row.CredentialEnv, true},
		{"credentialScheme", schemeOf(row.CredentialScheme), false},
		{"baseEnv", row.BaseEnv, false},
		{"gitRemote", row.GitRemote, false},
		{"gitRemoteEnv", row.GitRemoteEnv, false},
	} {
		if kv.val == "" && !kv.always {
			continue
		}
		val, err := marshalPlain(kv.val)
		if err != nil {
			return err
		}
		fmt.Fprintf(buf, "    %q: %s,\n", kv.key, val)
	}
	buf.WriteString("    \"routes\": [")
	for i, r := range row.Routes {
		line, err := marshalPlain(r)
		if err != nil {
			return err
		}
		sep := "\n"
		if i > 0 {
			sep = ",\n"
		}
		fmt.Fprintf(buf, "%s      %s", sep, line)
	}
	if len(row.Routes) > 0 {
		buf.WriteString("\n    ")
	}
	buf.WriteString("]\n  }")
	return nil
}

// marshalPlain is json.Marshal without HTML escaping, so a path's & or <
// stays readable in the written file.
func marshalPlain(v any) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}
