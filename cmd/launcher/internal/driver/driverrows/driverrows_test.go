package driverrows_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/driverrows"
)

const fixturesEnv = "SPINDRIFT_CONNECTOR_FIXTURES_DIR"

// fixtureWasm returns the path of a compiled fixture Connector, skipping the
// test when the fixtures dir is not provided.
func fixtureWasm(t *testing.T, name string) string {
	t.Helper()
	dir := os.Getenv(fixturesEnv)
	if dir == "" {
		t.Skipf("%s is unset; it names the dir holding the fixture Connector .wasm files", fixturesEnv)
	}
	return filepath.Join(dir, name)
}

func rows(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// natives are the stand-in native Strategies a test links in.
func natives(names ...string) []driverkit.Strategy {
	var out []driverkit.Strategy
	for _, n := range names {
		out = append(out, driverkit.Strategy{Name: n})
	}
	return out
}

// generated is the Descriptor the generated table holds for name.
func generated(t *testing.T, name string) driver.Descriptor {
	t.Helper()
	d, ok := driver.NewSet().DescriptorFor(name)
	if !ok {
		t.Fatalf("%s has no generated Descriptor", name)
	}
	return d
}

// inProcess is the Launcher's sandbox seam backed by the in-process engine.
var inProcess driverrows.Loader = connectorsandboxtest.InProcess().Load

// hosted is the Launcher's Set for raw: parsed against ns, Connectors loaded
// through the in-process engine.
func hosted(t *testing.T, raw json.RawMessage, ns ...driverkit.Strategy) *driver.Set {
	t.Helper()
	r, err := driverrows.Parse(raw, ns)
	if err != nil {
		t.Fatal(err)
	}
	return r.Hosted(inProcess)
}

// parseFile reads raw back through the Box's row file.
func parseFile(t *testing.T, raw json.RawMessage, ns ...driverkit.Strategy) (driverrows.Rows, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rows.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return driverrows.ReadFile(path, ns)
}

// box is the Box's Set for raw: read from a row file, which gates on the
// first-party mark.
func box(t *testing.T, raw json.RawMessage, ns ...driverkit.Strategy) *driver.Set {
	t.Helper()
	r, err := parseFile(t, raw, ns...)
	if err != nil {
		t.Fatal(err)
	}
	return r.InProcess()
}

func TestNoRowsChangeNothing(t *testing.T) {
	t.Parallel()
	ns := natives("opencode")
	base := driver.NewSet(ns...)
	r, err := driverrows.ReadFile("", ns)
	if err != nil {
		t.Fatal(err)
	}
	sets := map[string]*driver.Set{"file": r.InProcess()}
	for name, raw := range map[string]json.RawMessage{"nil": nil, "empty": {}} {
		r, err := driverrows.Parse(raw, ns)
		if err != nil {
			t.Fatal(err)
		}
		sets[name] = r.Hosted(nil)
	}
	for name, s := range sets {
		if got, want := s.Names(), base.Names(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: Names() = %v, want %v", name, got, want)
		}
		got, _ := s.CredentialRules("opencode")
		if want, _ := base.CredentialRules("opencode"); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: opencode CredentialRules = %v, want %v", name, got, want)
		}
	}
}

func TestHostedLoadsConnectorDriverLazily(t *testing.T) {
	t.Parallel()
	wasm := fixtureWasm(t, "driver.wasm")
	s := hosted(t, rows(t, []map[string]any{{
		"name":   "fixture",
		"module": wasm,
		"credentials": []map[string]any{
			{"env": []string{"FIXTURE_KEY"}, "modelPrefix": "fx/", "remedy": "set FIXTURE_KEY"},
		},
	}}))
	d, err := s.New("fixture")
	if err != nil {
		t.Fatal(err)
	}
	ev, err := d.ResultEvent("hi")
	if err != nil {
		t.Fatal(err)
	}
	if want := "result hi\n"; string(ev) != want {
		t.Errorf("ResultEvent = %q, want %q", ev, want)
	}
	rules, ok := s.CredentialRules("fixture")
	want := []driver.CredentialRule{{Env: []string{"FIXTURE_KEY"}, ModelPrefix: "fx/", Remedy: "set FIXTURE_KEY"}}
	if !ok || !reflect.DeepEqual(rules, want) {
		t.Errorf("CredentialRules(fixture) = %v, %v; want %v", rules, ok, want)
	}
}

func TestReadFileReadsDocument(t *testing.T) {
	t.Parallel()
	wasm := fixtureWasm(t, "driver.wasm")
	doc := rows(t, []map[string]any{{"name": "filefixture", "module": wasm, "firstParty": true}})
	if !box(t, doc).Has("filefixture") {
		t.Fatal("the Box's Set registered no Driver")
	}
	if _, err := driverrows.ReadFile(filepath.Join(t.TempDir(), "absent.json"), nil); err == nil {
		t.Error("ReadFile of a missing file succeeded")
	}
}

func TestNativeStrategyWinsOverModule(t *testing.T) {
	t.Parallel()
	s := hosted(t, rows(t, []map[string]any{{
		"name":   "nativerow",
		"module": filepath.Join(t.TempDir(), "never-read.wasm"),
	}}), natives("nativerow")...)
	if _, err := s.New("nativerow"); err != nil {
		t.Errorf("New(nativerow) = %v, want the native Driver", err)
	}
}

func TestNameMismatchFailsOnNew(t *testing.T) {
	t.Parallel()
	wasm := fixtureWasm(t, "driver.wasm")
	s := hosted(t, rows(t, []map[string]any{{"name": "other", "module": wasm}}))
	_, err := s.New("other")
	if err == nil || !strings.Contains(err.Error(), "other") {
		t.Errorf("New(other) = %v, want an error naming the Driver", err)
	}
}

func TestParseRejectsUnknownKey(t *testing.T) {
	t.Parallel()
	if _, err := driverrows.Parse(json.RawMessage(`[{"name":"x","bogus":1}]`), nil); err == nil {
		t.Error("an unknown key was accepted")
	}
}

func TestParseRejectsModulelessUnknownDriver(t *testing.T) {
	t.Parallel()
	_, err := driverrows.Parse(json.RawMessage(`[{"name":"nomodule"}]`), nil)
	if err == nil || !strings.Contains(err.Error(), "nomodule") {
		t.Errorf("Parse = %v, want an error naming nomodule", err)
	}
}

func TestParseAcceptsModulelessNativeDriver(t *testing.T) {
	t.Parallel()
	s := hosted(t, json.RawMessage(`[{"name":"nativeonly","credentials":[{"env":["NATIVE_KEY"],"remedy":"set it"}]}]`), natives("nativeonly")...)
	rules, _ := s.CredentialRules("nativeonly")
	if len(rules) != 1 || rules[0].Env[0] != "NATIVE_KEY" {
		t.Errorf("rules = %v", rules)
	}
}

func TestParseRefusesRowChangingNativeDescriptor(t *testing.T) {
	t.Parallel()
	changed := generated(t, "opencode")
	changed.Credentials = []driver.CredentialRule{{Env: []string{"STOLEN"}, Remedy: "x"}}
	_, err := driverrows.Parse(rows(t, []driver.Descriptor{changed}), natives("opencode"))
	if err == nil || !strings.Contains(err.Error(), "opencode") {
		t.Fatalf("Parse = %v, want an error naming opencode", err)
	}
}

func TestParseAcceptsRowEqualToNativeDescriptor(t *testing.T) {
	t.Parallel()
	before := generated(t, "opencode")
	s := hosted(t, rows(t, []driver.Descriptor{before}), natives("opencode")...)
	if got, _ := s.DescriptorFor("opencode"); !reflect.DeepEqual(got, before) {
		t.Errorf("opencode Descriptor = %v, want %v", got, before)
	}
}

func TestParseValidatesWholeBeforeAnyRowApplies(t *testing.T) {
	t.Parallel()
	r, err := driverrows.Parse(json.RawMessage(`[{"name":"early","module":"/x.wasm"},{"name":"nomodule"}]`), nil)
	if err == nil {
		t.Fatal("Parse succeeded")
	}
	if r.Hosted(nil).Has("early") {
		t.Error("a row registered before the bad row was reached")
	}
}

func TestParseRejectsDuplicateRowNames(t *testing.T) {
	t.Parallel()
	_, err := driverrows.Parse(json.RawMessage(`[{"name":"dup","module":"/a.wasm"},{"name":"dup","module":"/b.wasm"}]`), nil)
	if err == nil || !strings.Contains(err.Error(), "dup") {
		t.Errorf("Parse = %v, want a duplicate-row error naming dup", err)
	}
}

func TestShadowRowRecordsModuleAndKeepsNativeDriver(t *testing.T) {
	t.Parallel()
	if _, ok := driver.NewSet(natives("nat")...).ShadowModule("nat"); ok {
		t.Fatal("nat has a shadow before any row")
	}
	s := hosted(t, json.RawMessage(`[{"name":"nat","module":"/m/nat.wasm","shadow":true}]`), natives("nat")...)
	if got, ok := s.ShadowModule("nat"); !ok || got != "/m/nat.wasm" {
		t.Errorf("ShadowModule(nat) = %q, %v, want /m/nat.wasm", got, ok)
	}
	after, err := s.New("nat")
	if _, native := s.NativeStrategy("nat"); err != nil || !native || after.Name() != "nat" {
		t.Errorf("New(nat) = %v, %v, want the native Driver", after, err)
	}
}

func TestShadowRowWithoutNativeDriverOrModuleIsSkippedNotFatal(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ raw, driver string }{
		"unknown":    {`[{"name":"nope","module":"/x.wasm","shadow":true}]`, "nope"},
		"connector":  {`[{"name":"lazy","module":"/x.wasm"},{"name":"lazy","module":"/x.wasm","shadow":true}]`, "lazy"},
		"moduleless": {`[{"name":"nat","shadow":true}]`, "nat"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := hosted(t, json.RawMessage(tc.raw), natives("nat")...)
			if reason, ok := s.ShadowSkipped(tc.driver); !ok || reason == "" {
				t.Errorf("ShadowSkipped = %q, %v, want a reason", reason, ok)
			}
			if _, ok := s.ShadowModule(tc.driver); ok {
				t.Error("a skipped row left a shadow behind")
			}
			if name == "unknown" && s.Has("nope") {
				t.Error("a skipped shadow row registered a Driver")
			}
		})
	}
}

// A shadow row is never worth failing the run over: one that disagrees with the
// native Descriptor or repeats a shadow row is skipped with a reason.
func TestMalformedShadowRowsAreSkippedNotFatal(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ raw, driver string }{
		// An empty Descriptor is not opencode's generated one.
		"descriptor": {`[{"name":"opencode","module":"/x.wasm","shadow":true}]`, "opencode"},
		"duplicate":  {`[{"name":"nat","module":"/x.wasm","shadow":true},{"name":"nat","module":"/y.wasm","shadow":true}]`, "nat"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := hosted(t, json.RawMessage(tc.raw), natives("nat", "opencode")...)
			if reason, ok := s.ShadowSkipped(tc.driver); !ok || reason == "" {
				t.Errorf("ShadowSkipped = %q, %v, want a reason", reason, ok)
			}
			if _, ok := s.ShadowModule(tc.driver); ok {
				t.Error("a skipped row left a shadow behind")
			}
		})
	}
}

func TestShadowRowBesideNativeRowOfSameNameIsFine(t *testing.T) {
	t.Parallel()
	s := hosted(t, json.RawMessage(`[{"name":"nat"},{"name":"nat","module":"/m.wasm","shadow":true}]`), natives("nat")...)
	if got, ok := s.ShadowModule("nat"); !ok || got != "/m.wasm" {
		t.Errorf("ShadowModule(nat) = %q, %v, want /m.wasm", got, ok)
	}
}

func TestBoxRefusesToLoadConnectorDriverNotFirstParty(t *testing.T) {
	t.Parallel()
	s := box(t, rows(t, []map[string]any{{"name": "outsider", "module": "/m/outsider.wasm"}}))
	if !s.Has("outsider") {
		t.Fatal("the Box's Set registered no Driver for the unmarked row")
	}
	_, err := s.New("outsider")
	if !errors.Is(err, driverrows.ErrNotFirstParty) || !strings.Contains(err.Error(), "outsider") {
		t.Fatalf("New(outsider) = %v, want ErrNotFirstParty naming outsider", err)
	}
}

func TestBoxUnmarkedRowDoesNotBlockOtherDrivers(t *testing.T) {
	t.Parallel()
	s := box(t, rows(t, []map[string]any{
		{"name": "outsider", "module": "/m/x.wasm"},
		{"name": "nat"},
	}), natives("nat")...)
	if _, err := s.New("nat"); err != nil {
		t.Errorf("New(nat) = %v, want it unaffected by the unmarked row", err)
	}
}

func TestBoxLoadsFirstPartyConnectorDriver(t *testing.T) {
	t.Parallel()
	wasm := fixtureWasm(t, "driver.wasm")
	s := box(t, rows(t, []map[string]any{{"name": "fixture", "module": wasm, "firstParty": true}}))
	d, err := s.New("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if ev, err := d.ResultEvent("hi"); err != nil || string(ev) != "result hi\n" {
		t.Errorf("ResultEvent = %q, %v", ev, err)
	}
}

func TestBoxAppliesFirstPartyShadowRow(t *testing.T) {
	t.Parallel()
	s := box(t, nativeShadowRow(t, true), natives("opencode")...)
	if got, ok := s.ShadowModule("opencode"); !ok || got != "/m/opencode.wasm" {
		t.Errorf("ShadowModule(opencode) = %q, %v, want /m/opencode.wasm", got, ok)
	}
}

func TestBoxSkipsShadowRowNotFirstParty(t *testing.T) {
	t.Parallel()
	s := box(t, nativeShadowRow(t, false), natives("opencode")...)
	reason, ok := s.ShadowSkipped("opencode")
	if !ok || !strings.Contains(reason, driverrows.ErrNotFirstParty.Error()) {
		t.Errorf("ShadowSkipped = %q, %v, want the not-first-party reason", reason, ok)
	}
	if _, ok := s.ShadowModule("opencode"); ok {
		t.Error("a skipped row left a shadow behind")
	}
}

func TestHostedAcceptsConnectorDriverNotFirstParty(t *testing.T) {
	t.Parallel()
	s := hosted(t, rows(t, []map[string]any{{"name": "outsider", "module": "/m/outsider.wasm"}}))
	if !s.Has("outsider") {
		t.Error("Hosted registered no Driver, want the Launcher to carry no first-party gate")
	}
}

// nativeShadowRow is opencode's shadow row, carrying its generated Descriptor so
// only the first-party mark decides whether it applies.
func nativeShadowRow(t *testing.T, firstParty bool) json.RawMessage {
	t.Helper()
	return rows(t, []driverrows.Row{{Descriptor: generated(t, "opencode"), Module: "/m/opencode.wasm", Shadow: true, FirstParty: firstParty}})
}

// nativePrimaryRow is opencode's primary row, carrying its generated Descriptor.
func nativePrimaryRow(t *testing.T, module string, firstParty bool) json.RawMessage {
	t.Helper()
	return rows(t, []driverrows.Row{{Descriptor: generated(t, "opencode"), Module: module, Primary: true, FirstParty: firstParty}})
}

// claude is a plain Connector row with no native Strategy behind it, so a
// Connector that cannot load fails the run with a named error and nothing
// falls back to a native Driver.
func TestClaudeConnectorLoadFailureFailsNewWithoutFallback(t *testing.T) {
	t.Parallel()
	desc := generated(t, "claude")
	notWasm := filepath.Join(t.TempDir(), "claude.wasm")
	if err := os.WriteFile(notWasm, []byte("not wasm"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, module := range map[string]string{
		"missing module":   filepath.Join(t.TempDir(), "absent.wasm"),
		"module not valid": notWasm,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := hosted(t, rows(t, []driverrows.Row{{Descriptor: desc, Module: module}}))
			if s.HasNative("claude") {
				t.Fatal("claude has a native Strategy to fall back to")
			}
			d, err := s.New("claude")
			if err == nil || !strings.Contains(err.Error(), `DRIVER "claude"`) {
				t.Fatalf("New(claude) = %v, %v, want the load failure naming DRIVER \"claude\"", d, err)
			}
		})
	}
}

// A claude row cannot be primary or shadow: there is no native Strategy to
// replace or to compare against.
func TestClaudePrimaryRowRefusedAndShadowRowSkipped(t *testing.T) {
	t.Parallel()
	desc := generated(t, "claude")
	primary := rows(t, []driverrows.Row{{Descriptor: desc, Module: "/m/claude.wasm", Primary: true, FirstParty: true}})
	shadow := rows(t, []driverrows.Row{{Descriptor: desc, Module: "/m/claude.wasm", Shadow: true, FirstParty: true}})
	t.Run("primary", func(t *testing.T) {
		t.Parallel()
		if _, err := driverrows.Parse(primary, nil); err == nil || !strings.Contains(err.Error(), `Driver "claude" is primary but has no native Strategy`) {
			t.Errorf("Parse = %v, want the no-native-Strategy refusal", err)
		}
	})
	t.Run("shadow", func(t *testing.T) {
		t.Parallel()
		s := hosted(t, shadow)
		if reason, ok := s.ShadowSkipped("claude"); !ok || reason == "" {
			t.Errorf("ShadowSkipped = %q, %v, want a reason", reason, ok)
		}
		if _, ok := s.ShadowModule("claude"); ok {
			t.Error("a claude shadow row left a shadow behind")
		}
	})
}

func TestBoxRefusesPrimaryRowNotFirstParty(t *testing.T) {
	t.Parallel()
	s := box(t, nativePrimaryRow(t, "/m/opencode.wasm", false), natives("opencode")...)
	_, err := s.New("opencode")
	if !errors.Is(err, driverrows.ErrNotFirstParty) || !strings.Contains(err.Error(), `DRIVER "opencode"`) {
		t.Fatalf("New(opencode) = %v, want ErrNotFirstParty naming DRIVER \"opencode\"", err)
	}
}

func TestPrimaryRowValidation(t *testing.T) {
	t.Parallel()
	desc := generated(t, "opencode")
	for name, tc := range map[string]struct {
		row  driverrows.Row
		want string
	}{
		"no module":          {driverrows.Row{Descriptor: desc, Primary: true}, `Driver "opencode" is primary but has no module`},
		"not native":         {driverrows.Row{Descriptor: driver.Descriptor{Name: "ghost"}, Module: "/m/g.wasm", Primary: true}, `Driver "ghost" is primary but has no native Strategy`},
		"shadow and primary": {driverrows.Row{Descriptor: desc, Module: "/m/c.wasm", Primary: true, Shadow: true}, `Driver "opencode" cannot be both shadow and primary`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			raw := rows(t, []driverrows.Row{tc.row})
			_, errHost := driverrows.Parse(raw, natives("opencode"))
			_, errBox := parseFile(t, raw, natives("opencode")...)
			for who, err := range map[string]error{"host": errHost, "box": errBox} {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("%s: err = %v, want it to contain %q", who, err, tc.want)
				}
			}
		})
	}
}

func TestPrimaryAndShadowRowsForSameDriverRefused(t *testing.T) {
	t.Parallel()
	desc := generated(t, "opencode")
	primary := driverrows.Row{Descriptor: desc, Module: "/m/opencode.wasm", Primary: true, FirstParty: true}
	shadow := driverrows.Row{Descriptor: desc, Module: "/m/opencode.wasm", Shadow: true, FirstParty: true}
	unmarked := shadow
	unmarked.FirstParty = false
	for name, doc := range map[string][]driverrows.Row{
		"primary then shadow":          {primary, shadow},
		"shadow then primary":          {shadow, primary},
		"primary then unmarked shadow": {primary, unmarked},
		"unmarked shadow then primary": {unmarked, primary},
		"primary then repeated shadow": {primary, shadow, shadow},
		"repeated shadow then primary": {shadow, shadow, primary},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			raw := rows(t, doc)
			_, errHost := driverrows.Parse(raw, natives("opencode"))
			_, errBox := parseFile(t, raw, natives("opencode")...)
			for who, err := range map[string]error{"host": errHost, "box": errBox} {
				if err == nil || !strings.Contains(err.Error(), `Driver "opencode" has both a primary row and a shadow row`) {
					t.Errorf("%s: err = %v, want the primary-and-shadow refusal", who, err)
				}
			}
		})
	}
}

func TestPrimaryRowReplacesNativeDriver(t *testing.T) {
	t.Parallel()
	wasm := fixtureWasm(t, "driver.wasm")
	raw := rows(t, []driverrows.Row{{Descriptor: driver.Descriptor{Name: "fixture"}, Module: wasm, Primary: true, FirstParty: true}})
	for name, build := range map[string]func() *driver.Set{
		"host": func() *driver.Set { return hosted(t, raw, natives("fixture")...) },
		"box":  func() *driver.Set { return box(t, raw, natives("fixture")...) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d, err := build().New("fixture")
			if err != nil {
				t.Fatal(err)
			}
			if ev, err := d.ResultEvent("hi"); err != nil || string(ev) != "result hi\n" {
				t.Errorf("ResultEvent = %q, %v, want the Connector's", ev, err)
			}
		})
	}
}

func TestRowWithoutPrimaryOmitsKey(t *testing.T) {
	t.Parallel()
	b := rows(t, driverrows.Row{Descriptor: driver.Descriptor{Name: "x"}})
	if strings.Contains(string(b), "primary") {
		t.Errorf("row JSON = %s, want no primary key", b)
	}
}

// The module is read but never compiled: the Launcher refuses before any
// in-process load, so a row it cannot sandbox fails closed rather than loading
// in its own address space.
func TestHostedLoadFailsClosedWithNoSandbox(t *testing.T) {
	t.Parallel()
	module := filepath.Join(t.TempDir(), "outsider.wasm")
	if err := os.WriteFile(module, []byte("not wasm"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := driverrows.Parse(rows(t, []map[string]any{{"name": "outsider", "module": module, "firstParty": true}}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Hosted(nil).New("outsider"); !errors.Is(err, connectorsandbox.ErrNoConnectorSandbox) {
		t.Fatalf("New(outsider) = %v, want ErrNoConnectorSandbox", err)
	}
}

// A first-party row's load reaches the in-process engine, which rejects the
// module, not the sandbox seam, which the Box has none of.
func TestBoxFirstPartyLoadBypassesTheSandbox(t *testing.T) {
	t.Parallel()
	module := filepath.Join(t.TempDir(), "first.wasm")
	if err := os.WriteFile(module, []byte("not wasm"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := box(t, rows(t, []map[string]any{{"name": "first", "module": module, "firstParty": true}}))
	_, err := s.New("first")
	if err == nil || errors.Is(err, connectorsandbox.ErrNoConnectorSandbox) || !strings.Contains(err.Error(), "load module") {
		t.Fatalf("New(first) = %v, want the in-process engine's rejection of the module", err)
	}
}

// A previous-minor Driver is refused at the handshake, and the error names the
// Driver row so an operator knows which one to rebuild.
func TestFirstPartyPreviousMinorDriverIsRefusedByName(t *testing.T) {
	t.Parallel()
	module := fixtureWasm(t, "previous-driver.wasm")
	s := box(t, rows(t, []map[string]any{{"name": "stale", "module": module, "firstParty": true}}))
	_, err := s.New("stale")
	if !errors.Is(err, connectorhost.ErrProtocolUnsupported) || !strings.Contains(err.Error(), `driver "stale"`) {
		t.Fatalf("New(stale) = %v, want ErrProtocolUnsupported naming the driver", err)
	}
}
