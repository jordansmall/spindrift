package forgejo_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
)

const connectorCredentialEnv = "SPINDRIFT_FORGEJO_CONNECTOR_TOKEN"

// useForgejoRow applies a forgejo backend row, shaped as the input document
// carries it, with its {base} routes resolved against baseURL.
func useForgejoRow(t *testing.T, baseURL string, adjust func(*backendrows.Row)) {
	t.Helper()
	path, table := declaredRoutes(t)
	row := backendrows.Row{
		Name: "forgejo", Module: path, Routes: table,
		CredentialEnv: connectorCredentialEnv, BaseEnv: forgejo.ForgejoBaseURLEnv, Primary: true,
	}
	adjust(&row)
	doc, err := json.Marshal([]backendrows.Row{row})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backendrows.Snapshot())
	if err := backendrows.Use(doc, func(k string) string {
		if k == forgejo.ForgejoBaseURLEnv {
			return baseURL
		}
		return ""
	}); err != nil {
		t.Fatal(err)
	}
}

// buildForge runs the production constructor for a Box that cannot push when
// readOnlyBox, with core's git token distinct from the Connector's credential.
func buildForge(srv *httptest.Server, readOnlyBox bool, mergeMethod string) (forge.CodeForge, error) {
	l := mapLookup(map[string]string{forgejo.ForgejoBaseURLEnv: srv.URL, forgejo.ForgejoTokenEnv: gitSecret})
	s := backendkit.Shared{RepoSlug: connectorRepo, BaseBranch: "main", GitUserName: "Test Bot", GitUserEmail: "bot@example.com", MergeMethod: mergeMethod}
	build := forgejo.Strategy.NewCodeForge
	if readOnlyBox {
		build = forgejo.Strategy.NewReadOnlyCodeForge
	}
	return build(s, l, forge.SanitizedParent{}, nil)
}

// productionForge builds the Code Forge the way the launcher does under a
// primary forgejo row pointing at the compiled Connector and srv. It returns
// the row's resolved Route table and the Connector's audit file, which core
// writes under the working directory's log directory.
func productionForge(t *testing.T, srv *httptest.Server, readOnlyBox bool) (forge.CodeForge, []connectorhost.Route, string, error) {
	t.Helper()
	useForgejoRow(t, srv.URL, func(*backendrows.Row) {})
	t.Setenv(connectorCredentialEnv, connectorToken)
	t.Cleanup(forgejo.SetConnectorClient(srv.Client()))
	dir := t.TempDir()
	t.Chdir(dir)
	cf, err := buildForge(srv, readOnlyBox, "")
	row, _ := backendrows.Lookup("forgejo")
	return cf, connectortracker.Routes(row.Routes, connectorRepo), filepath.Join(dir, ".spindrift", "logs", "connector-forgejo.audit.jsonl"), err
}

func nativeForgeType(cf forge.CodeForge) string { return reflect.TypeOf(cf).String() }

// A Connector that fails to load fails the construction with its error and
// builds no forge, native or otherwise, whether the Box can push or not.
func TestPrimaryRow_LoadFailureFailsConstruction(t *testing.T) {
	for _, readOnlyBox := range []bool{false, true} {
		srv := httptest.NewTLSServer(nil)
		t.Cleanup(srv.Close)
		useForgejoRow(t, srv.URL, func(r *backendrows.Row) { r.Module = filepath.Join(t.TempDir(), "absent.wasm") })
		t.Chdir(t.TempDir())

		cf, err := buildForge(srv, readOnlyBox, "")

		if err == nil || !strings.Contains(err.Error(), "read module") {
			t.Errorf("readOnlyBox=%v: err = %v, want the module read error", readOnlyBox, err)
		}
		if cf != nil {
			t.Errorf("readOnlyBox=%v: built a %s despite the load failure", readOnlyBox, nativeForgeType(cf))
		}
	}
}

func TestPrimaryRow_LoadErrorIsReturnedAsIs(t *testing.T) {
	boom := errors.New("connector would not load")
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	useForgejoRow(t, srv.URL, func(*backendrows.Row) {})
	t.Cleanup(forgejo.SetLoadRowConnector(func(context.Context, backendrows.Row, backendrows.Options) (*connectorhost.Connector, error) {
		return nil, boom
	}))
	t.Chdir(t.TempDir())

	if cf, err := buildForge(srv, false, ""); !errors.Is(err, boom) || cf != nil {
		t.Errorf("buildForge = (%v, %v), want (nil, the load error)", cf, err)
	}
}

// A request outside the row's Routes is refused by the host: the operation
// fails with that refusal, and the forge is the Connector's, never a native
// fallback that would have answered.
func TestPrimaryRow_RefusedRouteFailsTheOperation(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	useForgejoRow(t, srv.URL, func(r *backendrows.Row) {
		r.Routes = append(slicesDeleteMethod(r.Routes, "GET"), connectorhost.Route{Method: "GET", Host: backendrows.BaseHost, Path: "/unrelated"})
	})
	t.Setenv(connectorCredentialEnv, connectorToken)
	t.Cleanup(forgejo.SetConnectorClient(srv.Client()))
	t.Chdir(t.TempDir())

	cf, err := buildForge(srv, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := nativeForgeType(cf); strings.Contains(got, "forgejoCodeForge") {
		t.Fatalf("built %s, want the Connector's Code Forge", got)
	}
	_, err = cf.Probe()

	if !errors.Is(err, connectorhost.ErrCall) || !errors.Is(err, connectorhost.ErrRouteRefused) {
		t.Errorf("Probe = %v, want an error wrapping connectorhost.ErrCall and ErrRouteRefused", err)
	}
}

func slicesDeleteMethod(routes []connectorhost.Route, method string) []connectorhost.Route {
	var out []connectorhost.Route
	for _, r := range routes {
		if r.Method != method {
			out = append(out, r)
		}
	}
	return out
}

// The Connector merges by rebase only, so a MERGE_METHOD it cannot honour is
// refused rather than silently ignored.
func TestPrimaryRow_RefusesAMergeMethodTheConnectorCannotHonour(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	useForgejoRow(t, srv.URL, func(*backendrows.Row) {})
	for _, method := range []string{"squash", "merge"} {
		cf, err := buildForge(srv, false, method)
		if !errors.Is(err, forgejo.ErrPrimaryMergeMethod) || !strings.Contains(err.Error(), "MERGE_METHOD") || cf != nil {
			t.Errorf("MERGE_METHOD=%s: buildForge = (%v, %v), want (nil, ErrPrimaryMergeMethod)", method, cf, err)
		}
	}
}

// Without a primary row, the native adapter is built and no Connector loads.
func TestNoPrimaryRow_BuildsTheNativeForge(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	t.Cleanup(forgejo.SetLoadRowConnector(func(context.Context, backendrows.Row, backendrows.Options) (*connectorhost.Connector, error) {
		t.Error("a Connector loaded without a primary row")
		return nil, errors.New("unexpected load")
	}))
	for name, plain := range map[string]func(*backendrows.Row){
		"no row":     nil,
		"plain row":  func(r *backendrows.Row) { r.Primary = false },
		"shadow row": func(r *backendrows.Row) { r.Primary, r.Shadow, r.ShadowEvery = false, true, 1 },
	} {
		t.Run(name, func(t *testing.T) {
			if plain != nil {
				useForgejoRow(t, srv.URL, plain)
			}
			for _, readOnlyBox := range []bool{false, true} {
				cf, err := buildForge(srv, readOnlyBox, "squash")
				if err != nil {
					t.Fatal(err)
				}
				if got := nativeForgeType(cf); !strings.Contains(got, "forgejo") || strings.Contains(got, "connectorforge") {
					t.Errorf("readOnlyBox=%v: built %s, want the native forgejo adapter", readOnlyBox, got)
				}
			}
		})
	}
}
