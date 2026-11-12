// Package backendrows applies the backend rows of the launcher input document
// (ADR 0073, issue #6805): a network Connector's compiled module, Route table
// and credential knob. A backend without a row here is built from the generated
// registry as before. It is a package of its own because it imports
// connectorhost, which must not import the registry.
package backendrows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
	"sync"

	"spindrift.dev/launcher/internal/connectorhost"
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
	// BaseEnv names the knob env var holding the site's https base URL, for a
	// Connector whose host is the Consumer's own (a Jira site). A Route or
	// accepted Route whose Host is BaseHost takes the base URL's host and has
	// the base URL's path prefix prepended to its Path; Use resolves them, so a
	// stored Row carries concrete hosts.
	BaseEnv string `json:"baseEnv,omitempty"`
	// Shadow asks the launcher to run this Connector in sampled shadow mode
	// beside the native backend of the same name, answering every ShadowEvery-th
	// call per read operation. ShadowEvery is required with Shadow.
	Shadow      bool `json:"shadow,omitempty"`
	ShadowEvery int  `json:"shadowEvery,omitempty"`
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
// path's subtree; so a new host, method or
// path, or a broader path pattern, is a widening and a narrower one is not.
// The subtree test is PathSet.Admits' request-path match, sound here because Use
// validates both tables as canonical patterns (no blanket "/").
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

// coreBackends are the built-in backends a row may not replace. It mirrors the
// `core = true` rows of lib/backends/ (ADR 0068, decision 7).
var coreBackends = map[string]bool{"git": true, "local": true}

var (
	mu      sync.RWMutex
	applied = map[string]Row{}
)

// Use applies the rows in raw, a JSON array of Row, replacing any rows an
// earlier call applied; empty raw applies none, so it clears them. A row with
// no name, a repeated name, a row missing its module, credentialEnv or
// routes, a name that is a built-in backend and an invalid Route table are
// errors, as is an invalid non-empty accepted baseline. A row's BaseHost routes
// are resolved from lookup(BaseEnv) first, so validation sees the concrete
// table (resolveBase says when an unusable base is an error); BaseHost without
// a BaseEnv is an error. The credential is only required at request time (see
// Load).
// Rows are validated whole before any is stored.
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
	for _, row := range rows {
		resolved, unconfigured, err := resolveBase(row, lookup)
		if err == nil {
			err = validate(resolved, seen)
		}
		if err != nil {
			return fmt.Errorf("backend rows: %w", err)
		}
		if !unconfigured {
			next[resolved.Name] = resolved
		}
	}
	mu.Lock()
	defer mu.Unlock()
	applied = next
	return nil
}

// unsetHost is the placeholder base host an unconfigured non-Shadow row
// resolves against, so its routes still validate whole before it is dropped.
const unsetHost = "unset.invalid"

// resolveBase returns row with every BaseHost route host and path prefix
// replaced from the base URL its BaseEnv names. It runs before validate so
// ValidateRoutes judges the table the Connector will actually be held to.
// unconfigured reports an unset or unusable base on a non-Shadow row: it
// resolves against unsetHost and the caller drops it. A Consumer importing every
// Connector need not configure a tracker it does not use, and the row shares the
// native adapter's base knob, which accepts plain http, so it must not fail a
// Consumer that does not read through the Connector yet; a Shadow row opted in,
// so every base error stands.
func resolveBase(row Row, lookup func(string) string) (_ Row, unconfigured bool, _ error) {
	uses := func(routes []connectorhost.Route) bool {
		return slices.ContainsFunc(routes, func(r connectorhost.Route) bool { return r.Host == BaseHost })
	}
	if !uses(row.Routes) && !uses(row.AcceptedRoutes) {
		return row, false, nil
	}
	if row.BaseEnv == "" {
		return row, false, fmt.Errorf("backend %q: a route host is %s but the row has no baseEnv", row.Name, BaseHost)
	}
	raw := lookup(row.BaseEnv)
	u, perr := url.Parse(raw)
	if perr != nil {
		u = &url.URL{} // url.Parse returns nil on error
	}
	prefix := strings.TrimSuffix(u.Path, "/")
	var err error
	switch {
	case raw == "":
		err = fmt.Errorf("backend %q: %s is not set", row.Name, row.BaseEnv)
	case perr != nil:
		err = fmt.Errorf("backend %q: %s is not a URL", row.Name, row.BaseEnv)
	case u.Scheme != "https":
		err = fmt.Errorf("backend %q: %s must be an https URL", row.Name, row.BaseEnv)
	case u.User != nil:
		err = fmt.Errorf("backend %q: %s must not carry userinfo", row.Name, row.BaseEnv)
	case u.RawPath != "":
		err = fmt.Errorf("backend %q: %s must not carry an escaped path", row.Name, row.BaseEnv)
	case strings.ContainsAny(raw, "?#"):
		err = fmt.Errorf("backend %q: %s must not carry a query or fragment", row.Name, row.BaseEnv)
	case !bareHost(u):
		err = fmt.Errorf("backend %q: %s must name a host", row.Name, row.BaseEnv)
	case !canonicalPrefix(prefix):
		err = fmt.Errorf("backend %q: %s must have a canonical path", row.Name, row.BaseEnv)
	}
	if err != nil {
		if row.Shadow {
			return row, false, err
		}
		u, prefix, unconfigured = &url.URL{Scheme: "https", Host: unsetHost}, "", true
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
	return row, unconfigured, nil
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
	case row.CredentialScheme != "" && row.CredentialScheme != SchemeBearer && row.CredentialScheme != SchemeBasic:
		return fmt.Errorf("backend %q: unknown credentialScheme %q, want bearer or basic", row.Name, row.CredentialScheme)
	case row.CredentialScheme == SchemeBasic && row.IdentityEnv == "":
		return fmt.Errorf("backend %q: the basic credentialScheme needs an identityEnv", row.Name)
	case row.CredentialScheme != SchemeBasic && row.IdentityEnv != "":
		return fmt.Errorf("backend %q: identityEnv applies only to the basic credentialScheme", row.Name)
	case row.ShadowEvery < 0:
		return fmt.Errorf("backend %q has a negative shadowEvery", row.Name)
	case row.ShadowEvery > 0 && !row.Shadow:
		return fmt.Errorf("backend %q has shadowEvery without shadow", row.Name)
	case row.Shadow && row.ShadowEvery == 0:
		return fmt.Errorf("backend %q has shadow without shadowEvery", row.Name)
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
	// Audit receives one line per request; required.
	Audit io.Writer
	// Client is the transport; nil means a default client.
	Client *http.Client
	// Shadow marks every audit entry as a shadow Connector's.
	Shadow bool
	// Observe, when non-nil, sees each audit entry after it is recorded.
	Observe func(connectorhost.AuditEntry)
}

// Load compiles the row's module as a network Connector bound to its Route
// table. The credential is read at each request, not at load, and a missing one
// fails the request before it is sent.
func Load(ctx context.Context, row Row, o Options) (*connectorhost.Connector, error) {
	if o.Audit == nil {
		return nil, fmt.Errorf("backend %q: no audit writer", row.Name)
	}
	getenv := o.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	wasm, err := os.ReadFile(row.Module)
	if err != nil {
		return nil, fmt.Errorf("backend %q: read module: %w", row.Name, err)
	}
	return connectorhost.LoadNetwork(ctx, wasm, connectorhost.Network{
		Name:   row.Name,
		Routes: row.Routes,
		Authorize: func(r *http.Request) error {
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
		},
		Client: o.Client,
		Audit:  connectorhost.NewAuditLog(o.Audit),
		Shadow: o.Shadow, Observe: o.Observe,
	})
}

// Snapshot captures the applied rows and returns the function that restores
// them, for a test that applies rows to the process-global set. It is not in
// export_test.go because cmd/launcher's own tests need it.
func Snapshot() (restore func()) {
	mu.Lock()
	defer mu.Unlock()
	saved := maps.Clone(applied)
	return func() {
		mu.Lock()
		defer mu.Unlock()
		applied = saved
	}
}
