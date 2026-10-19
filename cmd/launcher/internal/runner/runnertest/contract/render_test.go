package contract

import (
	"io"
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

// stubHarness stands in for an adapter: Render stashes the Launch and Decode
// reads back whatever the stub's family would have rendered, minus drop.
type stubHarness struct {
	family string
	drop   string // Box field (or networkField) the stub fails to render
	hosts  hostFault
	last   Launch
}

func (s *stubHarness) Family() string { return s.family }

func (s *stubHarness) Render(_ *testing.T, l Launch) []string {
	s.last = l
	if l.Box.Output != nil && s.drop != "Output" {
		_, _ = l.Box.Output.Write([]byte(Stdout))
	}
	return []string{"stub"}
}

func (s *stubHarness) Decode(*testing.T, []string) Decoded {
	b, oci := s.last.Box, s.family == runner.KindOCI
	d := Decoded{Env: map[string]string{"BAKED": "x"}, Network: s.last.Network}
	keep := func(field string) bool { return s.drop != field }
	if oci && keep("Name") {
		d.Name = b.Name
	}
	if keep("Env") {
		for k, v := range b.Env {
			d.Env[k] = v
		}
	}
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
	if !oci && keep("ClosureGeneration") {
		d.ClosureGeneration = b.ClosureGeneration
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

func TestSharedAddHostRowWantsExactlyOneHost(t *testing.T) {
	r := rowNamed(t, "SignalSocket/tcp-add-host-shared")
	for _, hosts := range []hostFault{hostsTwice, hostsNone} {
		h := &stubHarness{family: runner.KindOCI, hosts: hosts}
		if err := checkRow(t, h, r); err == nil {
			t.Errorf("stub rendering hosts fault %d passed the shared add-host row", hosts)
		}
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
	r.only = &familyOnly{runner.KindBwrap, "reason"}
	err := checkRow(t, &stubHarness{family: runner.KindOCI}, r)
	if err == nil || !strings.Contains(err.Error(), "stale guard") {
		t.Errorf("err = %v, want a stale-guard failure", err)
	}
}

func TestValidateRejectsMalformedTables(t *testing.T) {
	noReason := rowNamed(t, "Name")
	noReason.only = &familyOnly{Family: runner.KindOCI}
	unknown := rowNamed(t, "Env/plain")
	unknown.field = "Bogus"

	for _, tc := range []struct {
		name       string
		rows       []row
		unrendered map[string]string
		want       string
	}{
		{"missing reason", append([]row{noReason}, table...), unrendered, "no Reason"},
		{"unknown field", append([]row{unknown}, table...), unrendered, "no facet"},
		{"untabled Box field", table, nil, "Issue"},
		{"unrendered without reason", table, map[string]string{"Issue": ""}, "no reason"},
		{"unrendered non-Box field", table, map[string]string{"Bogus": "r"}, "not a runner.Box field"},
		{"unrendered and tabled", table, map[string]string{"Env": "r"}, "both unrendered and tabled"},
	} {
		err := validate(tc.rows, tc.unrendered)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
	if err := validate(table, unrendered); err != nil {
		t.Errorf("shipped table invalid: %v", err)
	}
}
