//go:build integration

package npmfamily

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registryvocab"
)

// yarnBinEnv names a yarn berry binary; the goldens never run yarn, so only
// this test notices berry rejecting a Forwarder URL (issue #6408).
const yarnBinEnv = "SPINDRIFT_YARN_BIN"

func tarballOf(t *testing.T, name string) []byte {
	t.Helper()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	body := []byte(`{"name":"` + name + `","version":"1.0.0"}`)
	if err := tw.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

// stubRegistry serves one package under basePath on loopback and returns the
// port. It stands in for the Forwarder, which fronts the same npm protocol.
func stubRegistry(t *testing.T, basePath, name string) int {
	t.Helper()
	tarball := tarballOf(t, name)
	sum := sha1.Sum(tarball)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, basePath) {
		case name: // the decoded path: yarn sends a scoped name as @scope%2fname
			packument := map[string]any{
				"name":      name,
				"dist-tags": map[string]string{"latest": "1.0.0"},
				// Berry 4.x quarantines versions younger than its minimal age gate.
				"time": map[string]string{"1.0.0": "2020-01-01T00:00:00.000Z"},
				"versions": map[string]any{"1.0.0": map[string]any{
					"name":    name,
					"version": "1.0.0",
					"dist": map[string]string{
						"tarball": srv.URL + "/tarballs/pkg.tgz",
						"shasum":  hex.EncodeToString(sum[:]),
					},
				}},
			}
			_ = json.NewEncoder(w).Encode(packument)
		default:
			if r.URL.Path == "/tarballs/pkg.tgz" {
				_, _ = w.Write(tarball)
				return
			}
			t.Logf("stub: unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().(*net.TCPAddr).Port
}

func TestYarnBerryInstallsThroughForwarderURL(t *testing.T) {
	yarn := os.Getenv(yarnBinEnv)
	if yarn == "" {
		t.Skipf("%s unset", yarnBinEnv)
	}
	yarn, err := filepath.Abs(yarn)
	if err != nil {
		t.Fatal(err)
	}

	const prefix = "r0"
	tests := []struct {
		name    string
		subtree registryvocab.Subtree
		pkg     string
		scoped  bool
	}{
		{"default registry", registryvocab.Subtree{Ecosystem: "yarn", Path: "/yarn"}, "left-pad", false},
		{"scoped only", registryvocab.Subtree{Ecosystem: "yarn", Path: "/acme", Scoped: true}, "@acme/widget", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := "/" + prefix + tc.subtree.Path
			port := stubRegistry(t, base+"/", tc.pkg)
			routes := taggedRoute(prefix, tc.subtree)
			exports, _ := Yarn.EnvExports(port, "", stubGetenv(nil), routes)
			_, hasRegistry := ecosystem.ExportValue(exports, yarnBindingEnvVar)
			if hasRegistry == tc.scoped {
				t.Fatalf("%s exported = %v, want %v: %v", yarnBindingEnvVar, hasRegistry, !tc.scoped, exports)
			}

			install := func(t *testing.T, exports []ecosystem.EnvExport) (string, string, error) {
				project := t.TempDir()
				home := t.TempDir()
				write := func(file, content string) {
					if err := os.WriteFile(filepath.Join(project, file), []byte(content), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				write("package.json", `{"name":"app","private":true,"dependencies":{"`+tc.pkg+`":"1.0.0"}}`)
				write("yarn.lock", "")
				if tc.scoped {
					// The in-tree rewrite's shape: scope -> Forwarder URL.
					scope := strings.TrimPrefix(strings.Split(tc.pkg, "/")[0], "@")
					write(".yarnrc.yml", "npmScopes:\n  "+scope+":\n    npmRegistryServer: \"http://127.0.0.1:"+
						strconv.Itoa(port)+base+"\"\n")
				}
				cmd := exec.Command(yarn, "install")
				cmd.Dir = project
				// Hermetic: no ambient YARN_* or user config reaches yarn.
				cmd.Env = []string{
					"PATH=" + os.Getenv("PATH"),
					"HOME=" + home,
					"YARN_ENABLE_TELEMETRY=0",
					"YARN_ENABLE_GLOBAL_CACHE=0",
					"YARN_CACHE_FOLDER=" + filepath.Join(home, "cache"),
					"YARN_ENABLE_IMMUTABLE_INSTALLS=0",
					"YARN_NODE_LINKER=node-modules",
				}
				for _, e := range exports {
					cmd.Env = append(cmd.Env, e.Name+"="+e.Value)
				}
				out, err := cmd.CombinedOutput()
				return project, string(out), err
			}

			t.Run("with the Strategy's exports", func(t *testing.T) {
				project, out, err := install(t, exports)
				if err != nil {
					t.Fatalf("yarn install: %v\n%s", err, out)
				}
				installed := filepath.Join(project, "node_modules", filepath.FromSlash(tc.pkg), "package.json")
				if _, err := os.Stat(installed); err != nil {
					t.Errorf("package not installed: %v\n%s", err, out)
				}
			})

			t.Run("without the whitelist", func(t *testing.T) {
				_, out, err := install(t, withoutYarnWhitelist(exports))
				if err == nil || !strings.Contains(out, "YN0081") {
					t.Errorf("yarn install without the whitelist: err = %v, want a YN0081 failure\n%s", err, out)
				}
			})
		})
	}
}
