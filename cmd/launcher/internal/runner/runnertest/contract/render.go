package contract

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"maps"
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
	Env      map[string]string // every on-argv env entry, baked ones included; build it with PutEnv
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

// BakedSet names what an adapter renders whatever its Box.
type BakedSet struct {
	EnvKeys, MountTargets []string
}

// PutEnv records k=v in env, the way every decoder must: a key a launch
// renders twice is an error, not a silent overwrite.
func PutEnv(env map[string]string, k, v string) error {
	if _, dup := env[k]; dup {
		return fmt.Errorf("env %s rendered twice", k)
	}
	env[k] = v
	return nil
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
	// Baked lists the env keys and mount targets the adapter renders for l
	// regardless of its Box. The contract subtracts each listed entry
	// exactly once before comparing, so a duplicate of one still fails. They
	// are keyed by env key and mount target because their values vary per
	// closure generation and per run.
	Baked(l Launch) BakedSet
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
	{"Network/zero", networkField, network(""),
		&familyOnly{runner.KindOCI, "bwrap isolates every mode but host, the zero value included (BwrapNetwork), so pasta's --no-map-gw renders it identically to no-host-loopback"}},
	{"Network/no-host-loopback", networkField, network(runner.NetworkModeNoHostLoopback), nil},
	{"Network/none", networkField, network(runner.NetworkModeNone), nil},
	{"Network/host", networkField, network(runner.NetworkModeHost),
		&familyOnly{runner.KindBwrap, "OCI has no host-netns rendering; Config.OCINetwork treats host as open"}},
}

// facets compare what a decoder recovered against the spec exactly, after
// checkRow subtracts the harness's baked set, so a stray or repeated entry
// fails.
var facets = map[string]func(d Decoded, l Launch) error{
	"Name": func(d Decoded, l Launch) error {
		if d.Name != l.Box.Name {
			return fmt.Errorf("name = %q, want %q", d.Name, l.Box.Name)
		}
		return nil
	},
	"Env": launchWide,
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
	"DriverCacheDir": launchWide,
	"OutboxDir":      launchWide,
	"Sockets":        addHosts,
	"RegistryProxy":  addHosts,
	"SignalSocket":   addHosts,
	"ClosureGeneration": func(d Decoded, l Launch) error {
		want, got := l.Box.ClosureGeneration, d.ClosureGeneration
		if (want == nil) != (got == nil) || (want != nil && *want != *got) {
			return fmt.Errorf("ClosureGeneration = %+v, want %+v", got, want)
		}
		return nil
	},
	networkField: func(d Decoded, l Launch) error {
		// The zero value is specced as open: OCINetwork emits no --network
		// flag for it, so both families render it exactly as they render open.
		want := l.Network
		if want == "" {
			want = runner.NetworkModeOpen
		}
		if d.Network != want {
			return fmt.Errorf("network = %q, want %q", d.Network, want)
		}
		return nil
	},
}

// launchWide is the facet of a field whose whole rendering is env or mounts:
// checkRow's whole-launch env and mount equality is its check.
func launchWide(Decoded, Launch) error { return nil }

func isLaunchWide(f func(Decoded, Launch) error) bool {
	return reflect.ValueOf(f).Pointer() == reflect.ValueOf(launchWide).Pointer()
}

func env(d Decoded, l Launch) error {
	if !maps.Equal(d.Env, l.Box.Env) {
		return fmt.Errorf("env = %v, want %v", d.Env, l.Box.Env)
	}
	return nil
}

// ownedMounts is every bind the spec asks for, none of them read-only.
func ownedMounts(b runner.Box) []Mount {
	var want []Mount
	if b.DriverCacheDir != "" {
		want = append(want, Mount{b.DriverCacheDir, runnertest.DriverSessionCacheTarget, false})
	}
	if b.OutboxDir != "" {
		want = append(want, Mount{b.OutboxDir, outboxTarget, false})
	}
	for _, s := range b.Sockets {
		want = append(want, Mount{s.Source, s.Target, false})
	}
	return want
}

func compareMounts(a, b Mount) int {
	return cmp.Or(
		cmp.Compare(a.Target, b.Target),
		cmp.Compare(a.Source, b.Source),
		cmp.Compare(boolInt(a.ReadOnly), boolInt(b.ReadOnly)),
	)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// mounts wants the launch's binds to be exactly the ones the spec asks for,
// in any order.
func mounts(d Decoded, l Launch) error {
	got := slices.SortedFunc(slices.Values(d.Mounts), compareMounts)
	want := slices.SortedFunc(slices.Values(ownedMounts(l.Box)), compareMounts)
	if !slices.Equal(got, want) {
		return fmt.Errorf("mounts = %+v, want %+v", got, want)
	}
	return nil
}

// subtractBaked returns d without the harness's baked env and mounts, one
// mount per baked target. A baked entry the launch lacks is an error: the
// harness's declaration is stale.
func subtractBaked(d Decoded, baked BakedSet) (Decoded, error) {
	d.Env = maps.Clone(d.Env)
	for _, k := range baked.EnvKeys {
		if _, ok := d.Env[k]; !ok {
			return d, fmt.Errorf("baked env %s is not rendered: stale Baked declaration", k)
		}
		delete(d.Env, k)
	}
	d.Mounts = slices.Clone(d.Mounts)
	for _, target := range baked.MountTargets {
		i := slices.IndexFunc(d.Mounts, func(m Mount) bool { return m.Target == target })
		if i < 0 {
			return d, fmt.Errorf("baked mount %s is not rendered: stale Baked declaration", target)
		}
		d.Mounts = slices.Delete(d.Mounts, i, i+1)
	}
	return d, nil
}

// bakedCollision rejects a row whose spec names an env key or mount target
// the harness bakes, which subtraction would otherwise mask.
func bakedCollision(l Launch, baked BakedSet) error {
	for k := range l.Box.Env {
		if slices.Contains(baked.EnvKeys, k) {
			return fmt.Errorf("env key %s collides with a baked env key", k)
		}
	}
	for _, m := range ownedMounts(l.Box) {
		if slices.Contains(baked.MountTargets, m.Target) {
			return fmt.Errorf("mount target %s collides with a baked mount target", m.Target)
		}
	}
	return nil
}

// addHosts serves the three fields whose rendering is a socket bind, a
// host-gateway mapping, or both. The binds are checkRow's to pin, so this
// pins the mappings, and the empty ones on rows that carry none.
func addHosts(d Decoded, l Launch) error {
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
	if err := validate(t, rows, unrendered); err != nil {
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
// an unknown field, a Box field no row and no unrendered entry covers, or a
// network mode (the zero value included) no networkField row covers.
func validate(t *testing.T, rows []row, unrendered map[string]string) error {
	boxType := reflect.TypeOf(runner.Box{})
	covered := map[string]bool{}
	modes := map[runner.NetworkMode]bool{}
	for _, r := range rows {
		if r.only != nil && r.only.Reason == "" {
			return fmt.Errorf("row %s: only(%s) carries no Reason", r.name, r.only.Family)
		}
		if _, ok := facets[r.field]; !ok {
			return fmt.Errorf("row %s: no facet for field %q", r.name, r.field)
		}
		covered[r.field] = true
		if r.field == networkField {
			modes[r.launch(t).Network] = true
		}
	}
	for _, m := range append([]runner.NetworkMode{""}, runner.NetworkModes()...) {
		if !modes[m] {
			return fmt.Errorf("network mode %q has no row: add a %s row for it", m, networkField)
		}
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
		if runner.IsOffArgvKey(k) {
			return fmt.Errorf("row %s: env key %s is off-argv by design", r.name, k)
		}
	}
	baked := h.Baked(l)
	if err := bakedCollision(l, baked); err != nil {
		return fmt.Errorf("row %s: %w", r.name, err)
	}
	d, err := subtractBaked(h.Decode(t, h.Render(t, l)), baked)
	if err != nil {
		return fmt.Errorf("row %s: %w", r.name, err)
	}
	// No guard covers env or mounts, so every row owes the whole-launch
	// equality: a stray or repeated entry fails whichever field the row probes.
	if err := errors.Join(env(d, l), mounts(d, l)); err != nil {
		return fmt.Errorf("row %s: %w", r.name, err)
	}
	err = facets[r.field](d, l)
	if r.only != nil && r.only.Family != h.Family() {
		if err == nil {
			return fmt.Errorf("stale guard: %s renders %s though the row says only %s (%s); remove the guard",
				h.Family(), r.name, r.only.Family, r.only.Reason)
		}
		return nil
	}
	return err
}
