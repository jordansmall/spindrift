package escape

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/sandbox"
	"spindrift.dev/launcher/internal/fixturebuild"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/primaryrecord"
	"spindrift.dev/launcher/internal/runner"
)

// escapeCredentialEnv stands for the CredentialEnv of a backend row: a secret
// the Launcher holds and a sandboxed Connector must never see.
const escapeCredentialEnv = "SPINDRIFT_ESCAPE_BACKEND_TOKEN"

// escapeTimeout bounds a whole escape battery: each dial inside the probe waits
// up to its own connectorhost dialTimeout.
const escapeTimeout = 2 * time.Minute

// HostDir resolves the escape-probe connector-host (issue #7017) to its
// store directory, the path a sandbox binds or an image holds. A non-empty
// skipReason means nix is absent.
func HostDir() (dir, skipReason string, err error) {
	bin, skip, err := fixturebuild.EscapeConnectorHost()
	if skip != "" || err != nil {
		return "", skip, err
	}
	return filepath.Dir(filepath.Dir(bin)), "", nil
}

type escapeResult struct {
	OK        bool   `json:"ok"`
	Detail    string `json:"detail"`
	Truncated bool   `json:"truncated"`
}

// escapeTarget is one thing the escape fixture Connector tries to reach.
type escapeTarget struct {
	name, kind, arg string
	// reachable targets succeed from a process with no sandbox, so the control
	// run asserts they do; the rest may be absent or refuse on this host.
	reachable bool
}

// escapeTargets lists what must be out of a sandboxed Connector's reach. The
// reachable ones are made here, so the control can prove the probe reaches
// them.
func escapeTargets(t *testing.T) []escapeTarget {
	t.Helper()
	var ts []escapeTarget
	add := func(name, kind, arg string, reachable bool) {
		ts = append(ts, escapeTarget{name, kind, arg, reachable})
	}

	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	add("read a file of the host", "read", secret, true)

	add("tcp to the host loopback", "tcp", listen(t, "tcp", "127.0.0.1:0"), true)
	if ip := nonLoopbackIPv4(t); ip != "" {
		add("tcp to the host address", "tcp", listen(t, "tcp", ip+":0"), true)
	}
	// Best-effort: nothing is known to listen on the gateway's port 53, so the
	// host-address listener above is the controlled proof of host reach.
	if gw := defaultGateway(t); gw != "" {
		add("tcp to the default gateway", "tcp", net.JoinHostPort(gw, "53"), false)
	}
	add("tcp to an external address", "tcp", "1.1.1.1:443", false)

	// Not t.TempDir: a long test name overflows the unix socket path limit.
	dir, err := os.MkdirTemp("", "escape")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	add("unix socket of the host", "unix", listen(t, "unix", filepath.Join(dir, "s")), true)

	if home, err := os.UserHomeDir(); err == nil {
		// Reachable only where it exists: the Nix seam check runs the control
		// with a HOME that does not.
		_, statErr := os.Stat(home)
		add("read the home directory", "read", home, statErr == nil)
		add("list the ssh directory", "read", filepath.Join(home, ".ssh"), false)
		add("read an ssh key", "read", filepath.Join(home, ".ssh", "id_ed25519"), false)
	}
	socks := []string{"/run/podman/podman.sock", "/var/run/docker.sock", fmt.Sprintf("/run/user/%d/podman/podman.sock", os.Getuid())}
	for _, s := range socks {
		add("unix "+s, "unix", s, false)
	}

	// Another store path than the host's: the fixtures, which are also on the
	// host's disk.
	if fixtures, err := fixturebuild.ConnectorDir(); err != nil {
		t.Logf("no 'list another store path' target: %v", err)
	} else {
		add("list another store path", "read", fixtures, true)
	}
	return ts
}

// listen opens a listener whose address the probe dials; the kernel completes
// the handshake without an Accept.
func listen(t *testing.T, network, addr string) string {
	t.Helper()
	l, err := net.Listen(network, addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().String()
}

func nonLoopbackIPv4(t *testing.T) string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Logf("no 'tcp to the host address' target: %v", err)
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
			return ipn.IP.String()
		}
	}
	return ""
}

// defaultGateway is the IPv4 default route's gateway, or "" when there is none.
func defaultGateway(t *testing.T) string {
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		t.Logf("no 'tcp to the default gateway' target: %v", err)
	}
	for _, l := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(l)
		if len(f) < 3 || f[1] != "00000000" {
			continue
		}
		if g, err := strconv.ParseUint(f[2], 16, 32); err == nil {
			return net.IPv4(byte(g), byte(g>>8), byte(g>>16), byte(g>>24)).String()
		}
	}
	return ""
}

// escapeProber loads escape.wasm into s and returns the call it answers.
func escapeProber(ctx context.Context, t *testing.T, s *connectorhost.Sessions) func(kind, arg string) escapeResult {
	t.Helper()
	dir, err := fixturebuild.ConnectorDir()
	if err != nil {
		t.Fatal(err)
	}
	wasm, err := os.ReadFile(filepath.Join(dir, "escape.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Load(ctx, wasm)
	if err != nil {
		t.Fatalf("Load escape.wasm: %v", err)
	}
	return func(kind, arg string) escapeResult {
		t.Helper()
		var out escapeResult
		if err := c.Call(ctx, "escape", map[string]string{"kind": kind, "arg": arg}, &out); err != nil {
			t.Fatalf("escape %s %q: %v", kind, arg, err)
		}
		return out
	}
}

// nixClosure is the store paths the host at dir refers to, itself included.
func nixClosure(t *testing.T, dir string) []string {
	t.Helper()
	out, err := exec.Command("nix", "path-info", "-r", dir).Output()
	if err != nil {
		t.Fatalf("nix path-info -r %s: %v", dir, err)
	}
	return strings.Fields(string(out))
}

// storeOutsideClosure is the entries of a /nix/store listing that are not in
// closure.
func storeOutsideClosure(listing string, closure []string) []string {
	var extra []string
	for _, e := range strings.Fields(listing) {
		if !slices.Contains(closure, "/nix/store/"+e) {
			extra = append(extra, e)
		}
	}
	return extra
}

// Control runs the escape battery against the escape-probe connector-host
// at host (see HostDir) started without a sandbox, and asserts every
// reachable target is reached. It proves the probe's primitives work, so a
// failure under Sandbox is the sandbox's doing, not a broken probe.
func Control(t *testing.T, host string) {
	targets := escapeTargets(t)

	cmd := exec.Command(filepath.Join(host, "bin", "connector-host"))
	cmd.Env = append(os.Environ(), escapeCredentialEnv+"=sentinel")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), escapeTimeout)
	defer cancel()
	s, err := connectorhost.NewSessions(ctx, stdout, stdin, func() { _ = cmd.Process.Kill() }, cmd.Wait)
	if err != nil {
		t.Fatalf("NewSessions over the host: %v (stderr: %s)", err, stderr.String())
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	probe := escapeProber(ctx, t, s)

	for _, tg := range targets {
		if !tg.reachable {
			continue
		}
		if got := probe(tg.kind, tg.arg); !got.OK {
			t.Errorf("%s: %s %q = %+v, want it reached", tg.name, tg.kind, tg.arg, got)
		}
	}
	// Not the host's closure: a host from SPINDRIFT_CONNECTOR_HOST_ESCAPEPROBE
	// need not be a store path, and any entry beyond the host's own will do.
	if got := probe("read", "/nix/store"); !got.OK || len(storeOutsideClosure(got.Detail, []string{host})) == 0 {
		t.Errorf("list /nix/store = ok %v with %d bytes, want entries beyond the host", got.OK, len(got.Detail))
	}
	if got := probe("env", ""); !got.OK || !strings.Contains(got.Detail, escapeCredentialEnv+"=sentinel") {
		t.Errorf("env = %+v, want the credential the host was started with", got)
	}
}

// startEscapeSandbox starts r's Connector sandbox and returns its sessions,
// stopped at the end of the test.
func startEscapeSandbox(ctx context.Context, t *testing.T, r runner.Runner) *connectorhost.Sessions {
	t.Helper()
	name := runner.ConnectorSandboxPrefix + "escape"
	sb, err := r.StartConnectorSandbox(name)
	if err != nil {
		t.Fatalf("StartConnectorSandbox: %v", err)
	}
	s, err := connectorhost.NewSessions(ctx, sb.Stdout, sb.Stdin, func() { _ = r.Kill(name) }, sb.Wait)
	if err != nil {
		t.Fatalf("NewSessions over the sandbox: %v (stderr: %s)", err, sb.Stderr())
	}
	t.Cleanup(func() {
		_ = s.Close(context.Background())
		_ = r.Kill(name)
	})
	return s
}

// Sandbox runs the escape battery inside r's Connector sandbox, whose
// connector-host is the escape-probe build at host (see HostDir), and
// asserts nothing outside the sandbox is reachable: no file, socket, network
// or credential of the Launcher. Only the host binary, which the sandbox
// holds, may be read, and /nix/store may list only the host's closure.
func Sandbox(t *testing.T, r runner.Runner, host string) {
	targets := escapeTargets(t)
	closure := nixClosure(t, host)
	credentials := append(runner.OffArgvKeys(), escapeCredentialEnv)
	for _, k := range credentials {
		t.Setenv(k, "sentinel-"+k)
	}

	ctx, cancel := context.WithTimeout(context.Background(), escapeTimeout)
	defer cancel()
	probe := escapeProber(ctx, t, startEscapeSandbox(ctx, t, r))

	if got := probe("read", filepath.Join(host, "bin", "connector-host")); !got.OK {
		t.Fatalf("read the host binary = %+v, want the probe to run inside the sandbox holding it", got)
	}
	for _, tg := range targets {
		if got := probe(tg.kind, tg.arg); got.OK {
			t.Errorf("%s: %s %q = %+v, want it refused", tg.name, tg.kind, tg.arg, got)
		}
	}
	if got := probe("read", "/nix/store"); got.OK {
		if extra := storeOutsideClosure(got.Detail, closure); len(extra) > 0 {
			t.Errorf("/nix/store lists %d entries beyond the host's closure, e.g. %v", len(extra), extra[:min(len(extra), 3)])
		}
	}

	got := probe("env", "")
	if !got.OK || got.Truncated {
		t.Fatalf("env = %+v, want the whole dump: a cut one could hide a leaked variable", got)
	}
	launcherEnv := os.Environ()
	for _, kv := range strings.Split(got.Detail, "\n") {
		if kv == "" {
			continue
		}
		if k, _, _ := strings.Cut(kv, "="); slices.Contains(credentials, k) {
			t.Errorf("the sandbox holds credential %s", k)
		}
		if slices.Contains(launcherEnv, kv) {
			t.Errorf("the sandbox holds %q, a variable the Launcher had", kv)
		}
	}
}

// RouteRefusal loads a fixture network Connector through r's Connector
// sandbox the way a backend row does, has it request a host outside its Route
// table, and asserts the Launcher refuses it and audits the refusal itself. It
// changes the working directory (t.Chdir), so callers must not run in parallel.
func RouteRefusal(t *testing.T, r runner.Runner) {
	t.Setenv(escapeCredentialEnv, "sentinel")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()

	sb := sandbox.New(r, "escape")
	t.Cleanup(func() { _ = sb.Close(context.Background()) })
	t.Cleanup(connectorsandbox.Use(sb))

	dir, err := fixturebuild.ConnectorDir()
	if err != nil {
		t.Fatal(err)
	}
	const name = "escape-route"
	row := backendrows.Row{
		Name:          name,
		Module:        filepath.Join(dir, "network-host.wasm"),
		Routes:        []connectorhost.Route{{Method: "GET", Host: srv.Listener.Addr().String(), Path: "/repos"}},
		CredentialEnv: escapeCredentialEnv,
	}
	ctx, cancel := context.WithTimeout(context.Background(), escapeTimeout)
	defer cancel()
	// Load opens its audit file under the working directory's log directory.
	cwd := t.TempDir()
	t.Chdir(cwd)
	c, err := backendrows.Load(ctx, row, backendrows.Options{})
	if err != nil {
		t.Fatalf("Load through the sandbox: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })

	// The fixture rewrites the URL's host to one outside the Route table.
	params := map[string]string{"url": srv.URL + "/repos/x", "method": "GET"}
	if err := c.Call(ctx, "fetch", params, new(struct{})); !errors.Is(err, connectorhost.ErrRouteRefused) {
		t.Fatalf("fetch err = %v, want ErrRouteRefused", err)
	}
	audit, err := os.ReadFile(filepath.Join(hostpaths.LogDir(cwd), primaryrecord.Name(name, ".audit.jsonl")))
	if err != nil {
		t.Fatal(err)
	}
	var entries []connectorhost.AuditEntry
	for dec := json.NewDecoder(bytes.NewReader(audit)); dec.More(); {
		var e connectorhost.AuditEntry
		if err := dec.Decode(&e); err != nil {
			t.Fatalf("audit log %q: %v", audit, err)
		}
		entries = append(entries, e)
	}
	if len(entries) != 1 || entries[0].Decision != connectorhost.DecisionRefused || entries[0].Connector != name {
		t.Errorf("audit = %+v, want one refused entry of %q", entries, name)
	}
	if hits.Load() != 0 {
		t.Errorf("the backend saw %d requests, want none", hits.Load())
	}
}
