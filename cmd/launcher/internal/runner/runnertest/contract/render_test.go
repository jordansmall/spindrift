package contract

import (
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
)

type hostFault int

const (
	hostsFaithful hostFault = iota
	hostsTwice              // skips the dedup
	hostsNone               // renders no host
)

type prefetchFault int

const (
	prefetchFaithful prefetchFault = iota
	prefetchBaked                  // renders the baked value, ignoring the file
	prefetchEmpty                  // renders nothing
)

// stubHarness stands in for an adapter: Render stashes the Launch and Decode
// reads back whatever the stub's family would have rendered, minus drop.
type stubHarness struct {
	family   string
	drop     string // Box field (or networkField) the stub fails to render
	hosts    hostFault
	prefetch prefetchFault
	name     string             // decoded as the Name on either family, when non-empty
	network  runner.NetworkMode // overrides the decoded mode, when non-empty
	last     Launch

	extraMount *Mount            // a bind no spec asked for
	extraEnv   map[string]string // env no spec asked for
	dupMounts  bool              // renders every bind twice, baked one included
	skipBaked  bool              // renders neither baked entry
	ghostBaked bool              // declares baked entries it never renders

	wrongClosure *runner.AgentGeneration // decoded as the closure generation, when non-nil
	wrongHosts   []string                // decoded as the add-hosts, when non-nil
}

// bakedMount is the one bind the stub renders on every launch.
var bakedMount = Mount{"/baked-src", "/baked", false}

func (s *stubHarness) Family() string { return s.family }

func (s *stubHarness) Baked(Launch) BakedSet {
	baked := BakedSet{EnvKeys: []string{"BAKED"}, MountTargets: []string{bakedMount.Target}}
	if s.ghostBaked {
		baked.EnvKeys, baked.MountTargets = append(baked.EnvKeys, "GHOST"), append(baked.MountTargets, "/ghost")
	}
	return baked
}

func (s *stubHarness) Render(_ *testing.T, l Launch) []string {
	s.last = l
	if l.Box.Output != nil && s.drop != "Output" {
		_, _ = l.Box.Output.Write([]byte(Stdout))
	}
	return []string{"stub"}
}

func (s *stubHarness) Decode(t *testing.T, _ []string) Decoded {
	b, oci := s.last.Box, s.family == runner.KindOCI
	d := Decoded{Env: map[string]string{}, Network: s.last.Network}
	if !s.skipBaked {
		d.Env["BAKED"] = "x"
		d.Mounts = append(d.Mounts, bakedMount)
	}
	keep := func(field string) bool { return s.drop != field }
	if oci && keep("Name") {
		d.Name = b.Name
	}
	if s.name != "" {
		d.Name = s.name
	}
	if keep("Env") {
		for k, v := range b.Env {
			d.Env[k] = v
		}
	}
	maps.Copy(d.Env, s.extraEnv)
	if b.DriverCacheDir != "" && keep("DriverCacheDir") {
		d.Mounts = append(d.Mounts, Mount{b.DriverCacheDir, runnertest.DriverSessionCacheTarget, false})
	}
	if b.OutboxDir != "" && keep("OutboxDir") {
		d.Mounts = append(d.Mounts, Mount{b.OutboxDir, outboxTarget, false})
	}
	if keep("Sockets") && keep("RegistryProxy") && keep("SignalSocket") {
		for _, m := range b.Sockets {
			d.Mounts = append(d.Mounts, Mount{m.Source, m.Target, false})
		}
	}
	if s.extraMount != nil {
		d.Mounts = append(d.Mounts, *s.extraMount)
	}
	if s.dupMounts {
		d.Mounts = append(d.Mounts, d.Mounts...)
	}
	if oci && s.hosts != hostsNone {
		addHost := func(h string) {
			if s.hosts == hostsTwice || !slices.Contains(d.AddHosts, h) {
				d.AddHosts = append(d.AddHosts, h)
			}
		}
		if keep("RegistryProxy") && b.RegistryProxy.TCPAddHost {
			addHost(b.RegistryProxy.Endpoint.Host())
		}
		if keep("SignalSocket") && b.SignalSocket.TCPAddHost {
			addHost(b.SignalSocket.Endpoint.Host())
		}
	}
	if s.wrongHosts != nil {
		d.AddHosts = s.wrongHosts
	}
	if !oci && keep("ClosureGeneration") {
		d.ClosureGeneration = b.ClosureGeneration
		if g := b.ClosureGeneration; g != nil && g.PrefetchFile != "" {
			content, err := os.ReadFile(g.PrefetchFile)
			if err != nil {
				t.Fatal(err)
			}
			d.ClosurePrefetch = string(content)
			switch s.prefetch {
			case prefetchBaked:
				d.ClosurePrefetch = "echo ok"
			case prefetchEmpty:
				d.ClosurePrefetch = ""
			}
		}
	}
	if s.wrongClosure != nil {
		d.ClosureGeneration = s.wrongClosure
	}
	if d.Network == "" {
		d.Network = runner.NetworkModeOpen
	}
	if s.network != "" {
		d.Network = s.network
	}
	switch {
	case !keep("Network"):
		d.Network = ""
	case !oci && d.Network == runner.NetworkModeOpen:
		d.Network = runner.NetworkModeNoHostLoopback
	case oci && d.Network == runner.NetworkModeHost:
		d.Network = runner.NetworkModeOpen
	}
	return d
}

func TestRenderingPassesFaithfulHarnesses(t *testing.T) {
	for _, f := range []string{runner.KindOCI, runner.KindBwrap} {
		t.Run(f, func(t *testing.T) { Rendering(t, &stubHarness{family: f}) })
	}
}

func rowNamed(t *testing.T, name string) row {
	t.Helper()
	for _, r := range table {
		if r.name == name {
			return r
		}
	}
	t.Fatalf("no row %s", name)
	return row{}
}

func TestDroppedFieldFailsItsRows(t *testing.T) {
	for _, tc := range []struct{ row, drop, family string }{
		{"Name", "Name", runner.KindOCI},
		{"Env/plain", "Env", runner.KindOCI},
		{"Output/buffer", "Output", runner.KindOCI},
		{"DriverCacheDir/set", "DriverCacheDir", runner.KindOCI},
		{"OutboxDir/set", "OutboxDir", runner.KindOCI},
		{"Sockets/two", "Sockets", runner.KindOCI},
		{"RegistryProxy/tcp-add-host", "RegistryProxy", runner.KindOCI},
		{"SignalSocket/tcp-add-host", "SignalSocket", runner.KindOCI},
		{"ClosureGeneration/set", "ClosureGeneration", runner.KindBwrap},
		{"Network/none", networkField, runner.KindOCI},
	} {
		h := &stubHarness{family: tc.family, drop: tc.drop}
		if err := checkRow(t, h, rowNamed(t, tc.row)); err == nil {
			t.Errorf("%s: dropping %s passed", tc.row, tc.drop)
		}
	}
}

func TestStraysAndDuplicatesFail(t *testing.T) {
	for _, tc := range []struct {
		name, row string
		h         stubHarness
		want      string
	}{
		{"stray mount", "DriverCacheDir/set", stubHarness{extraMount: &Mount{"/s", "/stray", false}}, "mounts ="},
		{"stray mount on unset field", "OutboxDir/unset", stubHarness{extraMount: &Mount{"/s", outboxTarget, false}}, "mounts ="},
		{"stray mount on a non-mount row", "Name", stubHarness{extraMount: &Mount{"/s", "/stray", false}}, "mounts ="},
		{"duplicated owned mount", "Sockets/two", stubHarness{dupMounts: true}, "mounts ="},
		{"duplicated baked mount", "Name", stubHarness{extraMount: &bakedMount}, "mounts ="},
		{"every mount duplicated", "Network/none", stubHarness{dupMounts: true}, "mounts ="},
		{"stray env on Env row", "Env/plain", stubHarness{extraEnv: map[string]string{"STRAY": "x"}}, "env ="},
		{"stray env on a non-Env row", "Name", stubHarness{extraEnv: map[string]string{"STRAY": "x"}}, "env ="},
		{"wrong env value", "Env/plain", stubHarness{extraEnv: map[string]string{envKey: "other"}}, "env ="},
		{"unrendered baked entries", "Name", stubHarness{skipBaked: true}, "stale Baked declaration"},
		{"stale baked declaration", "Name", stubHarness{ghostBaked: true}, "stale Baked declaration"},
	} {
		h := tc.h
		h.family = runner.KindOCI
		err := checkRow(t, &h, rowNamed(t, tc.row))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s (%s): err = %v, want it to mention %q", tc.name, tc.row, err, tc.want)
		}
	}
}

func TestPutEnvRejectsRepeatedKey(t *testing.T) {
	env := map[string]string{}
	if err := PutEnv(env, "K", "one"); err != nil {
		t.Fatalf("first PutEnv: %v", err)
	}
	err := PutEnv(env, "K", "two")
	if err == nil || !strings.Contains(err.Error(), "rendered twice") {
		t.Errorf("repeated key: err = %v, want a rendered-twice failure", err)
	}
	if env["K"] != "one" {
		t.Errorf("env[K] = %q, repeated key overwrote the first value", env["K"])
	}
}

func TestBakedCollisionRejectsRow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		launch func(*testing.T) Launch
	}{
		{"env key", withBox(func(_ *testing.T, b *runner.Box) { b.Env = map[string]string{"BAKED": "y"} })},
		{"socket target", withBox(sockets([2]string{"a.sock", bakedMount.Target}))},
	} {
		r := row{tc.name, "Name", tc.launch, nil}
		err := checkRow(t, &stubHarness{family: runner.KindOCI}, r)
		if err == nil || !strings.Contains(err.Error(), "collides") {
			t.Errorf("%s: err = %v, want a baked-collision failure", tc.name, err)
		}
	}
}

func TestSharedAddHostRowWantsExactlyOneHost(t *testing.T) {
	r := rowNamed(t, "SignalSocket/tcp-add-host-shared")
	for _, hosts := range []hostFault{hostsTwice, hostsNone} {
		h := &stubHarness{family: runner.KindOCI, hosts: hosts}
		if err := checkRow(t, h, r); err == nil {
			t.Errorf("stub rendering hosts fault %d passed the shared add-host row", hosts)
		}
	}
}

func TestClosureGenerationRowsWantTheRenderedPrefetch(t *testing.T) {
	for _, tc := range []struct {
		row   string
		fault prefetchFault
	}{
		{"ClosureGeneration/set", prefetchBaked},
		{"ClosureGeneration/set", prefetchEmpty},
		{"ClosureGeneration/set-empty-prefetch", prefetchBaked},
	} {
		h := &stubHarness{family: runner.KindBwrap, prefetch: tc.fault}
		if err := checkRow(t, h, rowNamed(t, tc.row)); err == nil {
			t.Errorf("%s: prefetch fault %d passed", tc.row, tc.fault)
		}
	}
}

func TestClosureGenerationFacetRejectsUnreadablePrefetch(t *testing.T) {
	g := &runner.AgentGeneration{PrefetchFile: filepath.Join(t.TempDir(), "missing")}
	l := Launch{Box: runner.Box{ClosureGeneration: g}}
	if err := facets["ClosureGeneration"](Decoded{ClosureGeneration: g}, l); err == nil {
		t.Error("unreadable PrefetchFile passed")
	}
}

func TestOutputFacetRejectsNonBuffer(t *testing.T) {
	l := Launch{Box: runner.Box{Output: io.Discard}}
	if err := facets["Output"](Decoded{}, l); err == nil {
		t.Error("non-*bytes.Buffer Output passed vacuously")
	}
}

func TestStaleGuardFails(t *testing.T) {
	// This stub renders Name as bwrap would not, so a guard saying bwrap
	// never does is stale.
	r := rowNamed(t, "Name")
	r.only = &familyOnly{Family: runner.KindBwrap, Reason: "reason", Otherwise: absentName}
	err := checkRow(t, &stubHarness{family: runner.KindOCI}, r)
	if err == nil || !strings.Contains(err.Error(), "stale guard") {
		t.Errorf("err = %v, want a stale-guard failure", err)
	}
}

func TestWrongRenderingSatisfiesNoGuard(t *testing.T) {
	wrongGeneration := &runner.AgentGeneration{Generation: "wrong-gen"}
	for _, tc := range []struct {
		name, row, family string
		h                 stubHarness
	}{
		{"name on a family that renders none", "Name", runner.KindBwrap, stubHarness{name: "wrong-name"}},
		{"network alias to the wrong mode", "Network/open", runner.KindBwrap, stubHarness{network: runner.NetworkModeNone}},
		{"host aliased to the wrong mode", "Network/host", runner.KindOCI, stubHarness{network: runner.NetworkModeNone}},
		{"closure generation on a family that binds none", "ClosureGeneration/set", runner.KindOCI, stubHarness{wrongClosure: wrongGeneration}},
		{"add-host on a family that maps none", "RegistryProxy/tcp-add-host", runner.KindBwrap, stubHarness{wrongHosts: []string{"wrong-host"}}},
	} {
		h := tc.h
		h.family = tc.family
		err := checkRow(t, &h, rowNamed(t, tc.row))
		if err == nil || !strings.Contains(err.Error(), "neither as the row asks") {
			t.Errorf("%s (%s): err = %v, want a wrong-rendering failure", tc.name, tc.row, err)
		}
	}
}

func TestValidateRejectsMalformedTables(t *testing.T) {
	noOtherwise := rowNamed(t, "Name")
	noOtherwise.only = &familyOnly{Family: runner.KindOCI, Reason: "r"}
	noReason := rowNamed(t, "Name")
	noReason.only = &familyOnly{Family: runner.KindOCI}
	guardedLaunchWide := rowNamed(t, "Env/plain")
	guardedLaunchWide.only = &familyOnly{Family: runner.KindOCI, Reason: "r", Otherwise: absentName}
	unknown := rowNamed(t, "Env/plain")
	unknown.field = "Bogus"

	without := func(name string) []row {
		var rows []row
		for _, r := range table {
			if r.name != name {
				rows = append(rows, r)
			}
		}
		return rows
	}

	for _, tc := range []struct {
		name       string
		rows       []row
		unrendered map[string]string
		want       string
	}{
		{"missing reason", append([]row{noReason}, table...), unrendered, "no Reason"},
		{"missing otherwise", append([]row{noOtherwise}, table...), unrendered, "no Otherwise"},
		{"guard on a launch-wide field", append([]row{guardedLaunchWide}, table...), unrendered, "checkRow compares"},
		{"unknown field", append([]row{unknown}, table...), unrendered, "no facet"},
		{"untabled Box field", table, nil, "Issue"},
		{"unrendered without reason", table, map[string]string{"Issue": ""}, "no reason"},
		{"unrendered non-Box field", table, map[string]string{"Bogus": "r"}, "not a runner.Box field"},
		{"unrendered and tabled", table, map[string]string{"Env": "r"}, "both unrendered and tabled"},
		{"untabled network mode", without("Network/none"), unrendered, `network mode "none" has no row`},
		{"untabled zero network mode", without("Network/zero"), unrendered, `network mode "" has no row`},
	} {
		err := validate(t, tc.rows, tc.unrendered)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
	if err := validate(t, table, unrendered); err != nil {
		t.Errorf("shipped table invalid: %v", err)
	}
}
