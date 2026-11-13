package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
)

// noGH puts a gh on PATH that fails every call and records it, and returns a
// function reporting how many times it was run: the Connector-backed paths
// reach GitHub through the host's http_request call, never the gh CLI.
func noGH(t *testing.T) (calls func() int) {
	t.Helper()
	dir := prependFakeGH(t, "exit 99")
	return func() int {
		m, _ := filepath.Glob(filepath.Join(dir, "call-*.txt"))
		return len(m)
	}
}

// ghProbeStub scripts the gh the native Probe runs: `auth status` fails
// without a credential, `repo view` fails for a repository gh cannot see.
func ghProbeStub(t *testing.T, status int) {
	t.Helper()
	prependFakeGH(t, `case "$*" in
"auth status")
	[ `+strconv.Itoa(status)+` = 401 ] && { echo "You are not logged in" >&2; exit 1; }
	exit 0 ;;
"repo view"*)
	[ `+strconv.Itoa(status)+` = 404 ] && { echo "Could not resolve to a Repository" >&2; exit 1; }
	printf 'owner/repo\n' ;;
esac`)
}

func errClass(err error) [3]bool {
	return [3]bool{err == nil, errors.Is(err, forge.ErrAuthFailure), errors.Is(err, forge.ErrRepoNotFound)}
}

func TestConnector_ProbeMatchesNative(t *testing.T) {
	for _, status := range []int{200, 401, 404} {
		h := newConnectorHarness(t)
		if status != 200 {
			h.backend.listStatus = status
		}
		calls := noGH(t)
		gotSlug, gotErr := h.tr.Probe()
		if n := calls(); n != 0 {
			t.Errorf("status %d: the Connector's Probe ran gh %d times", status, n)
		}

		ghProbeStub(t, status)
		wantSlug, wantErr := NewExecClient(connectorRepo, testLabels).Probe()

		if gotSlug != wantSlug || errClass(gotErr) != errClass(wantErr) {
			t.Errorf("status %d: Connector Probe = (%q, %v), native (%q, %v)", status, gotSlug, gotErr, wantSlug, wantErr)
		}
		if status == 200 && (gotErr != nil || gotSlug != connectorRepo) {
			t.Errorf("good credential: Probe = (%q, %v), want (%q, nil)", gotSlug, gotErr, connectorRepo)
		}
		if status == 401 && !errors.Is(gotErr, forge.ErrAuthFailure) {
			t.Errorf("auth failure: Probe err = %v, want forge.ErrAuthFailure", gotErr)
		}
		if status == 404 && !errors.Is(gotErr, forge.ErrRepoNotFound) {
			t.Errorf("missing repo: Probe err = %v, want forge.ErrRepoNotFound", gotErr)
		}
	}
}

// A demand count is the same number whether the native adapter reads it through
// gh or the Connector through the host: the Dispatchable issues, pull requests
// excluded, however many items sit in other states.
func TestConnector_CountReadyMatchesNative(t *testing.T) {
	type item struct {
		num    string
		labels []string
		pull   bool
	}
	ready, active := []string{testLabels.Dispatchable}, []string{testLabels.InProgress}
	for _, fx := range []struct {
		name  string
		items []item
		want  int
	}{
		{"none", nil, 0},
		{"mixed", []item{
			{"1", ready, false}, {"2", ready, true}, {"3", active, false},
			{"4", ready, false}, {"5", nil, false}, {"6", ready, false},
		}, 3},
	} {
		t.Run(fx.name, func(t *testing.T) {
			h := newConnectorHarness(t)
			calls := noGH(t)
			for _, it := range fx.items {
				h.SeedIssue(forge.Issue{Number: it.num, Title: it.num, Labels: it.labels})
				h.mutate(it.num, func(i *restIssue) { i.pull = it.pull })
			}
			dc, ok := forge.As[forge.DemandCounter](h.tr)
			if !ok {
				t.Fatal("Connector tracker does not serve CountReady: it must advertise tracker.count")
			}
			got, err := dc.CountReady(false)
			if err != nil || got != fx.want {
				t.Fatalf("Connector CountReady = %d, %v; want %d", got, err, fx.want)
			}
			if n := calls(); n != 0 {
				t.Errorf("the Connector's CountReady ran gh %d times", n)
			}
			if h.backend.unexpected != nil {
				t.Errorf("unexpected backend requests: %v", h.backend.unexpected)
			}

			n := newGithubHarness(t)
			for _, it := range fx.items {
				n.SeedIssue(forge.Issue{Number: it.num, Title: it.num, Labels: it.labels})
				if it.pull {
					if err := os.WriteFile(filepath.Join(n.issueDir(it.num), "pull_request"), nil, 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			if want := countReady(t, n, false); got != want {
				t.Errorf("Connector CountReady = %d, native %d", got, want)
			}
		})
	}
}

func TestConnector_CountReadyFailsWhenTheListDoes(t *testing.T) {
	h := newConnectorHarness(t)
	h.backend.listStatus = 500
	dc, _ := forge.As[forge.DemandCounter](h.tr)
	if _, err := dc.CountReady(false); err == nil {
		t.Fatal("CountReady against a 500 succeeded")
	}
}

func TestConnector_ProbeIntervalMatchesNative(t *testing.T) {
	h := newConnectorHarness(t)
	dc, ok := forge.As[forge.DemandCounter](h.tr)
	if !ok {
		t.Fatal("Connector tracker does not serve the demand count")
	}
	if got := dc.ProbeInterval(); got != demandProbeInterval {
		t.Errorf("Connector ProbeInterval = %v, native %v", got, demandProbeInterval)
	}
}

// recover reads the terminal label a claim stripped from the timeline. The
// Connector returns the removed names and core picks the state, so the answer
// must be the native adapter's, which filters the same timeline through gh.
func TestConnector_PriorClaimStateMatchesNative(t *testing.T) {
	for _, fx := range []struct {
		name    string
		removed []string
		want    forge.DispatchState
		found   bool
	}{
		{"complete last", []string{testLabels.Failed, testLabels.Complete}, forge.Complete, true},
		{"failed last", []string{testLabels.Complete, testLabels.Failed}, forge.Failed, true},
		{"complete then claim strip", []string{testLabels.Complete, testLabels.Dispatchable}, forge.Complete, true},
		{"none found", []string{testLabels.Dispatchable, "bug"}, 0, false},
		{"no removals", nil, 0, false},
	} {
		t.Run(fx.name, func(t *testing.T) {
			h := newConnectorHarness(t)
			h.SeedIssue(forge.Issue{Number: "7", Title: "t"})
			h.mutate("7", func(i *restIssue) { i.unlabeled = fx.removed })
			calls := noGH(t)
			pr, ok := forge.As[forge.PriorClaimStateReader](h.tr)
			if !ok {
				t.Fatal("Connector tracker does not serve PriorClaimState: it must advertise tracker.removedLabels")
			}
			got, found, err := pr.PriorClaimState("7")
			if err != nil || found != fx.found || (found && got != fx.want) {
				t.Fatalf("Connector PriorClaimState = %v, %v, %v; want %v, %v", got, found, err, fx.want, fx.found)
			}
			if n := calls(); n != 0 {
				t.Errorf("the Connector's PriorClaimState ran gh %d times", n)
			}
			if h.backend.unexpected != nil {
				t.Errorf("unexpected backend requests: %v", h.backend.unexpected)
			}

			// The native adapter's gh --jq has already reduced the timeline to
			// the removed names, one a line.
			out := strings.Join(fx.removed, "\\n")
			prependFakeGH(t, `case "$*" in *timeline*) printf '`+out+`\n' ;; esac`)
			wantState, wantFound, err := NewExecClient(connectorRepo, testLabels).PriorClaimState("7")
			if err != nil || found != wantFound || (found && got != wantState) {
				t.Errorf("Connector PriorClaimState = (%v, %v), native (%v, %v, %v)", got, found, wantState, wantFound, err)
			}
		})
	}
}

func TestConnector_PriorClaimStateFailsWhenTheTimelineDoes(t *testing.T) {
	h := newConnectorHarness(t)
	pr, _ := forge.As[forge.PriorClaimStateReader](h.tr)
	if _, found, err := pr.PriorClaimState("99"); err == nil || found {
		t.Fatalf("PriorClaimState of a missing issue = %v, %v; want an error", found, err)
	}
}

// tokenBackend is the GitHub API as token introspection sees it: the GraphQL
// viewer query answers with the token's OAuth scopes header, and
// GET /installation/repositories lists the repository with its permissions.
// Anything else is unexpected: the Connector declares no other Route for either
// read.
type tokenBackend struct {
	*httptest.Server
	mu         sync.Mutex
	token      string
	userStatus int
	scopes     *string
	// permissions is the installation list's permissions object for the repository;
	// nil leaves the repository out of the list, "" leaves out its permissions.
	permissions *string
	seenAuth    []string
	unexpected  []string
}

func newTokenBackend(t *testing.T, token string) *tokenBackend {
	t.Helper()
	b := &tokenBackend{token: token, userStatus: http.StatusOK}
	b.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.seenAuth = append(b.seenAuth, r.Header.Get("Authorization"))
		var req struct{ Query string }
		if r.Method == http.MethodPost && r.URL.Path == "/graphql" {
			_ = json.NewDecoder(r.Body).Decode(&req)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/installation/repositories":
			repos := `{"full_name":"owner/other","permissions":{"push":true}}`
			if b.permissions != nil {
				repos += `,{"full_name":"` + connectorRepo + `"`
				if *b.permissions != "" {
					repos += `,"permissions":` + *b.permissions
				}
				repos += `}`
			}
			_, _ = w.Write([]byte(`{"total_count":2,"repositories":[` + repos + `]}`))
		case strings.Contains(req.Query, "viewer"):
			if b.scopes != nil {
				w.Header().Set("X-OAuth-Scopes", *b.scopes)
			}
			w.WriteHeader(b.userStatus)
			_, _ = w.Write([]byte(`{"data":{"viewer":{"login":"bot"}}}`))
		default:
			b.unexpected = append(b.unexpected, r.Method+" "+r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(b.Close)
	return b
}

// tokenCredentialEnv names the host env var the token under audit rides in, as
// a backend row's CredentialEnv does.
const tokenCredentialEnv = "TOKEN_UNDER_AUDIT"

// loadTokenConnector loads the github Connector the way a backend row does,
// with the token under audit as its credential.
func loadTokenConnector(t *testing.T, b *tokenBackend, token string) *connectorhost.Connector {
	t.Helper()
	path, routes := githubConnectorRoutes(t, b.Listener.Addr().String(), connectorRepo)
	conn, err := backendrows.Load(context.Background(), backendrows.Row{
		Name: "github", Module: path, Routes: routes, CredentialEnv: tokenCredentialEnv,
	}, backendrows.Options{
		Getenv: func(k string) string {
			if k == tokenCredentialEnv {
				return token
			}
			return ""
		},
		Audit:  io.Discard,
		Client: b.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// The Connector's token introspection reports each token shape's grant over
// the GitHub API, reaching it with the token under audit as its credential and
// never through gh.
func TestConnector_TokenGrant(t *testing.T) {
	str := func(s string) *string { return &s }
	// Held to the native adapter's constant, not a copy, so the two cannot drift.
	notIntrospectable := backendkit.TokenIntrospection{Reason: notIntrospectableReason}
	for _, tc := range []struct {
		name, token string
		userStatus  int
		scopes      *string
		permissions *string
		want        backendkit.TokenIntrospection
		wantErr     bool
		// reaches is whether the shape makes a request at all.
		reaches bool
	}{
		{name: "classic PAT with repo scope", token: "ghp_abc", scopes: str("read:org, repo"), reaches: true,
			want: backendkit.TokenIntrospection{Introspectable: true, WriteCapable: true, Scopes: []string{"read:org", "repo"}}},
		{name: "classic PAT with only read:org", token: "ghp_abc", scopes: str("read:org"), reaches: true,
			want: backendkit.TokenIntrospection{Introspectable: true, Scopes: []string{"read:org"}}},
		{name: "classic PAT with no scopes", token: "ghp_abc", scopes: str(""), reaches: true,
			want: backendkit.TokenIntrospection{Introspectable: true}},
		{name: "classic PAT with no scopes header", token: "ghp_abc", reaches: true,
			want: backendkit.TokenIntrospection{Introspectable: true}},
		{name: "OAuth token with public_repo", token: "gho_abc", scopes: str("public_repo"), reaches: true,
			want: backendkit.TokenIntrospection{Introspectable: true, WriteCapable: true, Scopes: []string{"public_repo"}}},
		{name: "installation token with push", token: "ghs_abc", permissions: str(`{"pull":true,"push":true}`), reaches: true,
			want: backendkit.TokenIntrospection{Introspectable: true, WriteCapable: true}},
		{name: "installation token with read only", token: "ghs_abc", permissions: str(`{"pull":true,"push":false}`), reaches: true,
			want: backendkit.TokenIntrospection{Introspectable: true}},
		{name: "installation token without permissions", token: "ghs_abc", permissions: str(""), reaches: true, wantErr: true},
		{name: "installation token without the repository", token: "ghs_abc", reaches: true, wantErr: true},
		{name: "fine-grained PAT", token: "github_pat_abc", want: notIntrospectable},
		{name: "unknown prefix", token: "ghu_abc", want: notIntrospectable},
		{name: "opaque token", token: "opaque", want: notIntrospectable},
		{name: "user read refused", token: "ghp_abc", userStatus: http.StatusUnauthorized, reaches: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newTokenBackend(t, tc.token)
			b.scopes, b.permissions = tc.scopes, tc.permissions
			if tc.userStatus != 0 {
				b.userStatus = tc.userStatus
			}
			conn := loadTokenConnector(t, b, tc.token)
			calls := noGH(t)
			got, err := connectorforge.IntrospectToken(conn, connectorforge.Target{API: b.URL, Repo: connectorRepo}, tc.token)
			if n := calls(); n != 0 {
				t.Errorf("the Connector's introspection ran gh %d times", n)
			}
			if len(b.unexpected) != 0 {
				t.Errorf("unexpected backend requests: %v", b.unexpected)
			}
			if tc.reaches != (len(b.seenAuth) > 0) {
				t.Errorf("backend saw %d requests, want some = %v", len(b.seenAuth), tc.reaches)
			}
			for _, a := range b.seenAuth {
				if a != "Bearer "+tc.token {
					t.Errorf("backend saw Authorization %q, want the token under audit", a)
				}
			}
			if (err != nil) != tc.wantErr || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("IntrospectToken = (%+v, %v), want (%+v, err=%v)", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

// The Connector carries its own copy of the write-scope set, since its module
// cannot import this package: each scope native treats as write-granting must
// make the Connector report a write-capable grant too.
func TestConnector_TokenGrantWriteScopesMatchNative(t *testing.T) {
	for scope := range ghTokenWriteScopes {
		t.Run(scope, func(t *testing.T) {
			b := newTokenBackend(t, "ghp_abc")
			b.scopes = &scope
			conn := loadTokenConnector(t, b, "ghp_abc")
			got, err := connectorforge.IntrospectToken(conn, connectorforge.Target{API: b.URL, Repo: connectorRepo}, "ghp_abc")
			if err != nil || !got.WriteCapable {
				t.Errorf("IntrospectToken(scope %q) = (%+v, %v), want write-capable", scope, got, err)
			}
		})
	}
}

// doctor's label check compares the lifecycle labels with what the repository
// defines. Both page REST, the Connector directly and native through gh; 150
// labels force a second page.
func TestConnector_ListLabelsMatchesNative(t *testing.T) {
	for _, fx := range []struct {
		name string
		n    int
	}{
		{"none", 0},
		{"one page", 3},
		{"past one page", 150},
	} {
		t.Run(fx.name, func(t *testing.T) {
			h := newConnectorHarness(t)
			var want []string
			for i := range fx.n {
				want = append(want, "label-"+strconv.Itoa(i))
			}
			h.backend.labels = want
			calls := noGH(t)
			got, err := h.tr.ListLabels()
			if err != nil {
				t.Fatalf("Connector ListLabels: %v", err)
			}
			if n := calls(); n != 0 {
				t.Errorf("the Connector's ListLabels ran gh %d times", n)
			}
			if h.backend.unexpected != nil {
				t.Errorf("unexpected backend requests: %v", h.backend.unexpected)
			}

			script := `if [ "$1" = "api" ] && [ "$2" = "repos/` + connectorRepo + `/labels?per_page=100" ]; then`
			for _, name := range want {
				script += "\n  echo " + name
			}
			prependFakeGH(t, script+"\n  exit 0\nfi\nexit 1\n")
			native, err := NewExecClient(connectorRepo, testLabels).ListLabels()
			if err != nil {
				t.Fatalf("native ListLabels: %v", err)
			}
			slices.Sort(got)
			slices.Sort(native)
			if !slices.Equal(got, native) || len(got) != fx.n {
				t.Errorf("Connector ListLabels = %d labels, native %d, want %d", len(got), len(native), fx.n)
			}
		})
	}
}
