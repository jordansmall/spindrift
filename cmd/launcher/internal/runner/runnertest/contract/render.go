package contract

import (
	"bytes"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
)

// Mount is one bind a decoder recovered.
type Mount struct {
	Source, Target string
	ReadOnly       bool
}

// Decoded is what a decoder recovers from one recorded launch argv.
type Decoded struct {
	Name     string
	Env      map[string]string // every on-argv env entry, baked ones included
	Mounts   []Mount           // every bind, baked ones included
	AddHosts []string          // hosts mapped to host-gateway
	Network  runner.NetworkMode
	// ClosureGeneration is nil when the argv binds no generation.
	ClosureGeneration *runner.AgentGeneration
}

// Launch is one row's input: a Box plus the adapter's network-mode config,
// which is not a Box field today (ADR 0069 decision 6 moves it into the
// shared plan).
type Launch struct {
	Box     runner.Box
	Network runner.NetworkMode
}

// RenderingHarness is what an adapter supplies to run the rendering contract.
type RenderingHarness interface {
	// Family is the harness's runner family: runner.KindOCI or runner.KindBwrap.
	Family() string
	// Render runs l.Box through the adapter's real Run under l.Network
	// against a recording fake CLI (runnertest.NewFakeCLI) whose launch
	// invocation prints Stdout to stdout, and returns that invocation's
	// recorded argv. The adapter must be configured with
	// runnertest.DriverSessionCacheTarget as its Driver session-cache dir.
	Render(t *testing.T, l Launch) []string
	// Decode parses argv from Render back into what it rendered.
	Decode(t *testing.T, argv []string) Decoded
}

const (
	// Stdout is what the fake CLI's launch invocation prints.
	Stdout = "contract-stdout-marker"
	// envKey must stay outside runner.OffArgvKeys: those values never reach
	// argv by design, which the per-adapter tests pin.
	envKey = "CONTRACT_PROBE"

	// outboxTarget mirrors the in-Box target runner.BuildMountSpecs binds
	// Box.OutboxDir at.
	outboxTarget = "/outbox"

	// networkField is the pseudo-field for the adapter's network-mode config,
	// which is not a Box field.
	networkField = "Network"
)

type familyOnly struct {
	Family string
	Reason string
}

type row struct {
	name  string
	field string // a runner.Box field name, or networkField
	// launch builds the row's input; every Box it returns has a Name.
	launch func(t *testing.T) Launch
	// only restricts the row to one family. For any other family the kit
	// asserts the facet does NOT hold, so a stale guard fails.
	only *familyOnly
}

// unrendered lists Box fields no adapter renders, each with the reason.
var unrendered = map[string]string{
	"Issue": "neither adapter reads it; Name carries the Box identity",
}

func minimal() Launch { return Launch{Box: runner.Box{Name: boxName}} }

func withBox(f func(t *testing.T, b *runner.Box)) func(t *testing.T) Launch {
	return func(t *testing.T) Launch {
		l := minimal()
		f(t, &l.Box)
		return l
	}
}

func network(m runner.NetworkMode) func(*testing.T) Launch {
	return func(*testing.T) Launch { l := minimal(); l.Network = m; return l }
}

func sockets(pairs ...[2]string) func(*testing.T, *runner.Box) {
	return func(t *testing.T, b *runner.Box) {
		for _, p := range pairs {
			b.Sockets = append(b.Sockets, runner.SocketMount{
				Source: runnertest.NewTestSocket(t, p[0]),
				Target: p[1],
			})
		}
	}
}

var (
	tcpHost = registrymanifest.NewTCPEndpoint("host.docker.internal", "8080")

	ociOnlyAddHost = &familyOnly{runner.KindOCI, "bwrap's registry transport is always unix (RegistryProxyTransport returns a unix endpoint), and the Signal socket takes that same verdict (dispatch/box.go passes it to startSignalSocket), so bwrap never needs a host-gateway mapping"}
)

var table = []row{
	{"Name", "Name", withBox(func(*testing.T, *runner.Box) {}),
		&familyOnly{runner.KindOCI, "bwrap keys a Box by its cgroup dir and running map, never argv"}},
	{"Env/plain", "Env", withBox(func(_ *testing.T, b *runner.Box) {
		b.Env = map[string]string{envKey: "one", envKey + "_2": "two"}
	}), nil},
	{"Output/buffer", "Output", withBox(func(_ *testing.T, b *runner.Box) { b.Output = &bytes.Buffer{} }), nil},
	{"DriverCacheDir/set", "DriverCacheDir", withBox(func(t *testing.T, b *runner.Box) {
		b.DriverCacheDir = t.TempDir()
	}), nil},
	{"DriverCacheDir/unset", "DriverCacheDir", withBox(func(*testing.T, *runner.Box) {}), nil},
	{"OutboxDir/set", "OutboxDir", withBox(func(t *testing.T, b *runner.Box) {
		b.OutboxDir = t.TempDir()
	}), nil},
	{"OutboxDir/unset", "OutboxDir", withBox(func(*testing.T, *runner.Box) {}), nil},
	{"Sockets/single", "Sockets", withBox(sockets([2]string{"a.sock", "/run/contract/a.sock"})), nil},
	{"Sockets/two", "Sockets", withBox(sockets(
		[2]string{"a.sock", "/run/contract/a.sock"},
		[2]string{"b.sock", "/run/contract/b.sock"},
	)), nil},
	{"RegistryProxy/unix", "RegistryProxy", withBox(func(t *testing.T, b *runner.Box) {
		b.RegistryProxy.Endpoint = registrymanifest.NewUnixEndpoint(runner.RegistryProxySocketTarget)
		sockets([2]string{"registry.sock", runner.RegistryProxySocketTarget})(t, b)
	}), nil},
	{"RegistryProxy/tcp-add-host", "RegistryProxy", withBox(func(_ *testing.T, b *runner.Box) {
		b.RegistryProxy = runner.RegistryProxyLocation{Endpoint: tcpHost, TCPSecret: "s", TCPAddHost: true}
	}), ociOnlyAddHost},
	{"RegistryProxy/tcp-no-add-host", "RegistryProxy", withBox(func(_ *testing.T, b *runner.Box) {
		b.RegistryProxy = runner.RegistryProxyLocation{Endpoint: tcpHost, TCPSecret: "s"}
	}), nil},
	{"SignalSocket/unix", "SignalSocket", withBox(sockets([2]string{"signal.sock", runner.SignalSocketTarget})), nil},
	{"SignalSocket/tcp-add-host", "SignalSocket", withBox(func(_ *testing.T, b *runner.Box) {
		b.SignalSocket = runner.SignalSocketLocation{Endpoint: tcpHost, TCPAddHost: true}
	}), ociOnlyAddHost},
	{"SignalSocket/tcp-add-host-shared", "SignalSocket", withBox(func(_ *testing.T, b *runner.Box) {
		// The production shape: both share one transport verdict, so one host.
		b.RegistryProxy = runner.RegistryProxyLocation{Endpoint: tcpHost, TCPSecret: "s", TCPAddHost: true}
		b.SignalSocket = runner.SignalSocketLocation{Endpoint: tcpHost, TCPAddHost: true}
	}), ociOnlyAddHost},
	{"SignalSocket/tcp-no-add-host", "SignalSocket", withBox(func(_ *testing.T, b *runner.Box) {
		b.SignalSocket = runner.SignalSocketLocation{Endpoint: tcpHost}
	}), nil},
	{"ClosureGeneration/unset", "ClosureGeneration", withBox(func(*testing.T, *runner.Box) {}), nil},
	{"ClosureGeneration/set", "ClosureGeneration", withBox(func(_ *testing.T, b *runner.Box) {
		// PrefetchFile stays empty: bwrap renders its content, not its path.
		b.ClosureGeneration = &runner.AgentGeneration{
			AgentFiles:    "/nix/store/contract-agent-files",
			AgentEnv:      "/nix/store/contract-agent-env",
			NixConfigFile: "/nix/store/contract-nix-conf",
			Generation:    "contract-gen-1",
		}
	}), &familyOnly{runner.KindBwrap, "OCI boots a baked image and never binds a closure generation"}},
	{"Network/open", networkField, network(runner.NetworkModeOpen),
		&familyOnly{runner.KindOCI, "bwrap's pasta namespace always runs --no-map-gw (ADR 0042), so open renders identically to no-host-loopback"}},
	{"Network/no-host-loopback", networkField, network(runner.NetworkModeNoHostLoopback), nil},
	{"Network/none", networkField, network(runner.NetworkModeNone), nil},
	{"Network/host", networkField, network(runner.NetworkModeHost),
		&familyOnly{runner.KindBwrap, "OCI has no host-netns rendering; networkArg treats host as open"}},
}

// facets compare what a decoder recovered against only what the spec asks
// for, so baked env and mounts never pollute the verdict.
var facets = map[string]func(d Decoded, l Launch) error{
	"Name": func(d Decoded, l Launch) error {
		if d.Name != l.Box.Name {
			return fmt.Errorf("name = %q, want %q", d.Name, l.Box.Name)
		}
		return nil
	},
	"Env": func(d Decoded, l Launch) error {
		for k, want := range l.Box.Env {
			if got, ok := d.Env[k]; !ok || got != want {
				return fmt.Errorf("env %s = %q (present=%v), want %q", k, got, ok, want)
			}
		}
		return nil
	},
	"Output": func(_ Decoded, l Launch) error {
		buf, ok := l.Box.Output.(*bytes.Buffer)
		if !ok {
			return fmt.Errorf("Output is %T, want *bytes.Buffer", l.Box.Output)
		}
		if !bytes.Contains(buf.Bytes(), []byte(Stdout)) {
			return fmt.Errorf("Output = %q, want it to contain %q", buf.String(), Stdout)
		}
		return nil
	},
	"DriverCacheDir": func(d Decoded, l Launch) error {
		return wantMountOrNone(d, l.Box.DriverCacheDir, runnertest.DriverSessionCacheTarget)
	},
	"OutboxDir": func(d Decoded, l Launch) error {
		return wantMountOrNone(d, l.Box.OutboxDir, outboxTarget)
	},
	"Sockets":       socketsAndAddHosts,
	"RegistryProxy": socketsAndAddHosts,
	"SignalSocket":  socketsAndAddHosts,
	"ClosureGeneration": func(d Decoded, l Launch) error {
		want, got := l.Box.ClosureGeneration, d.ClosureGeneration
		if (want == nil) != (got == nil) || (want != nil && *want != *got) {
			return fmt.Errorf("ClosureGeneration = %+v, want %+v", got, want)
		}
		return nil
	},
	networkField: func(d Decoded, l Launch) error {
		if d.Network != l.Network {
			return fmt.Errorf("network = %q, want %q", d.Network, l.Network)
		}
		return nil
	},
}

func wantMount(d Decoded, m Mount) error {
	if !slices.Contains(d.Mounts, m) {
		return fmt.Errorf("no mount %+v in %+v", m, d.Mounts)
	}
	return nil
}

// wantMountOrNone wants source bound at target when the Box sets source, and
// nothing at target when it does not.
func wantMountOrNone(d Decoded, source, target string) error {
	if source != "" {
		return wantMount(d, Mount{Source: source, Target: target})
	}
	for _, m := range d.Mounts {
		if m.Target == target {
			return fmt.Errorf("unset field still renders mount %+v", m)
		}
	}
	return nil
}

// socketsAndAddHosts serves the three fields whose rendering is a socket
// bind, a host-gateway mapping, or both: which one a row exercises follows
// from what its Box carries.
func socketsAndAddHosts(d Decoded, l Launch) error {
	for _, s := range l.Box.Sockets {
		if err := wantMount(d, Mount{Source: s.Source, Target: s.Target}); err != nil {
			return err
		}
	}
	// Registry proxy then Signal socket, as oci's addHostTargets orders them.
	// They share one transport verdict, so the normal TCP case maps one host.
	var want []string
	if p := l.Box.RegistryProxy; p.TCPAddHost {
		want = append(want, p.Endpoint.Host())
	}
	if s := l.Box.SignalSocket; s.TCPAddHost && !slices.Contains(want, s.Endpoint.Host()) {
		want = append(want, s.Endpoint.Host())
	}
	if !slices.Equal(d.AddHosts, want) {
		return fmt.Errorf("add-hosts = %v, want %v", d.AddHosts, want)
	}
	return nil
}

// Rendering runs the rendering contract against h: every Box field a runner
// renders on argv reads back as the spec asked, or is explicitly named as
// unrendered, or is a statement about one family only.
func Rendering(t *testing.T, h RenderingHarness) {
	t.Helper()
	rendering(t, h, table, unrendered)
}

func rendering(t *testing.T, h RenderingHarness, rows []row, unrendered map[string]string) {
	t.Helper()
	if err := validate(rows, unrendered); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if err := checkRow(t, h, r); err != nil {
				t.Error(err)
			}
		})
	}
}

// validate fails on a malformed table: a guard without a reason, a row for
// an unknown field, or a Box field no row and no unrendered entry covers.
func validate(rows []row, unrendered map[string]string) error {
	boxType := reflect.TypeOf(runner.Box{})
	covered := map[string]bool{}
	for _, r := range rows {
		if r.only != nil && r.only.Reason == "" {
			return fmt.Errorf("row %s: only(%s) carries no Reason", r.name, r.only.Family)
		}
		if _, ok := facets[r.field]; !ok {
			return fmt.Errorf("row %s: no facet for field %q", r.name, r.field)
		}
		covered[r.field] = true
	}
	for name, reason := range unrendered {
		if _, ok := boxType.FieldByName(name); !ok {
			return fmt.Errorf("unrendered %q is not a runner.Box field", name)
		}
		if reason == "" {
			return fmt.Errorf("unrendered %q carries no reason", name)
		}
		if covered[name] {
			return fmt.Errorf("Box field %s is both unrendered and tabled", name)
		}
		covered[name] = true
	}
	for i := range boxType.NumField() {
		if name := boxType.Field(i).Name; !covered[name] {
			return fmt.Errorf("runner.Box field %s has no row and is not listed unrendered: add a rendering row or an unrendered entry with a reason", name)
		}
	}
	return nil
}

func checkRow(t *testing.T, h RenderingHarness, r row) error {
	t.Helper()
	l := r.launch(t)
	if l.Box.Name == "" {
		return fmt.Errorf("row %s: launch has no Box.Name", r.name)
	}
	for k := range l.Box.Env {
		if runner.OffArgvKeys[k] {
			return fmt.Errorf("row %s: env key %s is off-argv by design", r.name, k)
		}
	}
	d := h.Decode(t, h.Render(t, l))
	err := facets[r.field](d, l)
	if r.only != nil && r.only.Family != h.Family() {
		if err == nil {
			return fmt.Errorf("stale guard: %s renders %s though the row says only %s (%s); remove the guard",
				h.Family(), r.name, r.only.Family, r.only.Reason)
		}
		return nil
	}
	return err
}
