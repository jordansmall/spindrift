package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const acceptRows = `[{"name":"forgejo","module":"/m.wasm","credentialEnv":"FORGEJO_TOKEN","baseEnv":"FORGEJO_BASE_URL",
"routes":[{"method":"GET","host":"{base}","path":"/api/v1/orgs"}]}]`

const acceptGolden = `{
  "forgejo": {
    "credentialEnv": "FORGEJO_TOKEN",
    "credentialScheme": "bearer",
    "baseEnv": "FORGEJO_BASE_URL",
    "routes": [
      {"method":"GET","host":"{base}","path":"/api/v1/orgs"}
    ]
  }
}
`

func TestConnectorsAcceptWritesTheBaselineFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	var stdout, stderr bytes.Buffer
	if code := cmdConnectorsAccept(json.RawMessage(acceptRows), []string{path}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != acceptGolden {
		t.Errorf("file = %s\nwant %s", got, acceptGolden)
	}
	if out := stdout.String(); !strings.Contains(out, path) {
		t.Errorf("stdout = %q, want it to name the file", out)
	}
}

func TestConnectorsAcceptWritesDeclaredHeaders(t *testing.T) {
	rows := `[{"name":"forgejo","module":"/m.wasm","credentialEnv":"FORGEJO_TOKEN",
"routes":[{"method":"GET","host":"h.test","path":"/p","requestHeaders":["X-Req"],"responseHeaders":["X-Resp"]}]}]`
	path := filepath.Join(t.TempDir(), "baseline.json")
	var stdout, stderr bytes.Buffer
	if code := cmdConnectorsAccept(json.RawMessage(rows), []string{path}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"requestHeaders":["X-Req"]`, `"responseHeaders":["X-Resp"]`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("file lacks %s:\n%s", want, got)
		}
	}
}

func TestConnectorsAcceptWritesDeclaredDocuments(t *testing.T) {
	rows := `[{"name":"github","module":"/m.wasm","credentialEnv":"GH_TOKEN",
"routes":[{"method":"POST","host":"h.test","path":"/graphql","documents":{"Q":"query { a }"}}]}]`
	path := filepath.Join(t.TempDir(), "baseline.json")
	var stdout, stderr bytes.Buffer
	if code := cmdConnectorsAccept(json.RawMessage(rows), []string{path}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"documents":{"Q":"query { a }"}`; !strings.Contains(string(got), want) {
		t.Errorf("file lacks %s:\n%s", want, got)
	}
}

func TestConnectorsAcceptDefaultsToTheConsumerFlakeRoot(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	var stdout, stderr bytes.Buffer
	if code := cmdConnectorsAccept(json.RawMessage(acceptRows), nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "spindrift-connector-routes.json")); err != nil {
		t.Errorf("default baseline not written: %v", err)
	}
}

func TestConnectorsAcceptRefusesBadArgsAndEmptyDocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.json")
	cases := map[string]struct {
		rows string
		args []string
		want string
	}{
		"two files":      {acceptRows, []string{path, "extra"}, "usage: spindrift connectors"},
		"flag":           {acceptRows, []string{"--force"}, "usage: spindrift connectors"},
		"no rows":        {"", []string{path}, "no Connector"},
		"unparsable row": {`{`, []string{path}, "backend rows"},
	}
	for name, c := range cases {
		var stdout, stderr bytes.Buffer
		if code := cmdConnectorsAccept(json.RawMessage(c.rows), c.args, &stdout, &stderr); code == 0 {
			t.Errorf("%s: exit 0, want nonzero", name)
		}
		if !strings.Contains(stderr.String(), c.want) {
			t.Errorf("%s: stderr = %q, want %q", name, stderr.String(), c.want)
		}
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a refused run wrote the baseline")
	}
}

func TestConnectorsVerbRequiresTheAcceptSubverb(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := verbHandlers["connectors"](nil, []string{"bogus"}, &stdout, &stderr); code == 0 {
		t.Error("exit 0, want nonzero")
	}
	if !strings.Contains(stderr.String(), "usage: spindrift connectors accept") {
		t.Errorf("stderr = %q", stderr.String())
	}
}
