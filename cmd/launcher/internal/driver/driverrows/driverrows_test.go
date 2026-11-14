package driverrows_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/driverrows"
	"spindrift.dev/launcher/internal/driver/drivertest"
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

func TestUseNilChangesNothing(t *testing.T) {
	names := driver.Names()
	rules, _ := driver.CredentialRules("claude")
	for _, raw := range []json.RawMessage{nil, {}} {
		if err := driverrows.Use(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := driverrows.UseFile(""); err != nil {
		t.Fatal(err)
	}
	if got := driver.Names(); !reflect.DeepEqual(got, names) {
		t.Errorf("Names() = %v, want %v", got, names)
	}
	if got, _ := driver.CredentialRules("claude"); !reflect.DeepEqual(got, rules) {
		t.Errorf("claude CredentialRules = %v, want %v", got, rules)
	}
}

func TestUseLoadsConnectorDriverLazily(t *testing.T) {
	drivertest.Isolate(t)
	wasm := fixtureWasm(t, "driver.wasm")
	err := driverrows.Use(rows(t, []map[string]any{{
		"name":   "fixture",
		"module": wasm,
		"credentials": []map[string]any{
			{"env": []string{"FIXTURE_KEY"}, "modelPrefix": "fx/", "remedy": "set FIXTURE_KEY"},
		},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	d, err := driver.New("fixture")
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
	rules, ok := driver.CredentialRules("fixture")
	want := []driver.CredentialRule{{Env: []string{"FIXTURE_KEY"}, ModelPrefix: "fx/", Remedy: "set FIXTURE_KEY"}}
	if !ok || !reflect.DeepEqual(rules, want) {
		t.Errorf("CredentialRules(fixture) = %v, %v; want %v", rules, ok, want)
	}
}

func TestUseFileReadsDocument(t *testing.T) {
	drivertest.Isolate(t)
	wasm := fixtureWasm(t, "driver.wasm")
	path := filepath.Join(t.TempDir(), "rows.json")
	doc := rows(t, []map[string]any{{"name": "filefixture", "module": wasm, "firstParty": true}})
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := driverrows.UseFile(path); err != nil {
		t.Fatal(err)
	}
	if !driver.Has("filefixture") {
		t.Fatal("UseFile registered no Driver")
	}
	if err := driverrows.UseFile(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Error("UseFile of a missing file succeeded")
	}
}

func TestNativeStrategyWinsOverModule(t *testing.T) {
	drivertest.Isolate(t)
	driver.Register(driverkit.Strategy{Name: "nativerow"})
	err := driverrows.Use(rows(t, []map[string]any{{
		"name":   "nativerow",
		"module": filepath.Join(t.TempDir(), "never-read.wasm"),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.New("nativerow"); err != nil {
		t.Errorf("New(nativerow) = %v, want the native Driver", err)
	}
}

func TestNameMismatchFailsOnNew(t *testing.T) {
	drivertest.Isolate(t)
	wasm := fixtureWasm(t, "driver.wasm")
	if err := driverrows.Use(rows(t, []map[string]any{{"name": "other", "module": wasm}})); err != nil {
		t.Fatal(err)
	}
	_, err := driver.New("other")
	if err == nil || !strings.Contains(err.Error(), "other") {
		t.Errorf("New(other) = %v, want an error naming the Driver", err)
	}
}

func TestUseRejectsUnknownKey(t *testing.T) {
	if err := driverrows.Use(json.RawMessage(`[{"name":"x","bogus":1}]`)); err == nil {
		t.Error("an unknown key was accepted")
	}
}

func TestUseRejectsModulelessUnknownDriver(t *testing.T) {
	err := driverrows.Use(json.RawMessage(`[{"name":"nomodule"}]`))
	if err == nil || !strings.Contains(err.Error(), "nomodule") {
		t.Errorf("Use = %v, want an error naming nomodule", err)
	}
}

func TestUseAcceptsModulelessNativeDriver(t *testing.T) {
	drivertest.Isolate(t)
	driver.Register(driverkit.Strategy{Name: "nativeonly"})
	rowsJSON := json.RawMessage(`[{"name":"nativeonly","credentials":[{"env":["NATIVE_KEY"],"remedy":"set it"}]}]`)
	if err := driverrows.Use(rowsJSON); err != nil {
		t.Fatal(err)
	}
	rules, _ := driver.CredentialRules("nativeonly")
	if len(rules) != 1 || rules[0].Env[0] != "NATIVE_KEY" {
		t.Errorf("rules = %v", rules)
	}
}

func TestUseRefusesRowChangingNativeDescriptor(t *testing.T) {
	drivertest.Isolate(t)
	driver.Register(driverkit.Strategy{Name: "claude"})
	before, ok := driver.DescriptorFor("claude")
	if !ok {
		t.Fatal("claude has no Descriptor")
	}
	changed := before
	changed.Credentials = []driver.CredentialRule{{Env: []string{"STOLEN"}, Remedy: "x"}}
	err := driverrows.Use(rows(t, []driver.Descriptor{changed}))
	if err == nil || !strings.Contains(err.Error(), "claude") {
		t.Fatalf("Use = %v, want an error naming claude", err)
	}
	if got, _ := driver.DescriptorFor("claude"); !reflect.DeepEqual(got, before) {
		t.Errorf("claude Descriptor = %v, want it unchanged: %v", got, before)
	}
}

func TestUseAcceptsRowEqualToNativeDescriptor(t *testing.T) {
	drivertest.Isolate(t)
	driver.Register(driverkit.Strategy{Name: "claude"})
	before, _ := driver.DescriptorFor("claude")
	if err := driverrows.Use(rows(t, []driver.Descriptor{before})); err != nil {
		t.Fatalf("Use = %v, want the generated Descriptor accepted", err)
	}
	if got, _ := driver.DescriptorFor("claude"); !reflect.DeepEqual(got, before) {
		t.Errorf("claude Descriptor = %v, want %v", got, before)
	}
}

func TestUseRefusesPartialApplyOnLaterBadRow(t *testing.T) {
	drivertest.Isolate(t)
	err := driverrows.Use(json.RawMessage(`[{"name":"early","module":"/x.wasm"},{"name":"nomodule"}]`))
	if err == nil {
		t.Fatal("Use succeeded")
	}
	if driver.Has("early") {
		t.Error("a row registered before the bad row was reached")
	}
}

func TestUseRejectsDuplicateRowNames(t *testing.T) {
	drivertest.Isolate(t)
	err := driverrows.Use(json.RawMessage(`[{"name":"dup","module":"/a.wasm"},{"name":"dup","module":"/b.wasm"}]`))
	if err == nil || !strings.Contains(err.Error(), "dup") {
		t.Errorf("Use = %v, want a duplicate-row error naming dup", err)
	}
	if driver.Has("dup") {
		t.Error("a duplicate document registered a Driver")
	}
}

func TestShadowRowRecordsModuleAndKeepsNativeDriver(t *testing.T) {
	drivertest.Isolate(t)
	driver.Register(driverkit.Strategy{Name: "nat"})
	if _, ok := driver.ShadowModule("nat"); ok {
		t.Fatal("nat has a shadow before any row")
	}
	if err := driverrows.Use(json.RawMessage(`[{"name":"nat","module":"/m/nat.wasm","shadow":true}]`)); err != nil {
		t.Fatal(err)
	}
	if got, ok := driver.ShadowModule("nat"); !ok || got != "/m/nat.wasm" {
		t.Errorf("ShadowModule(nat) = %q, %v, want /m/nat.wasm", got, ok)
	}
	after, err := driver.New("nat")
	if _, native := driver.NativeStrategy("nat"); err != nil || !native || after.Name() != "nat" {
		t.Errorf("driver.New(nat) = %v, %v, want the native Driver", after, err)
	}
}

func TestShadowRowWithoutNativeDriverOrModuleIsSkippedNotFatal(t *testing.T) {
	drivertest.Isolate(t)
	driver.Register(driverkit.Strategy{Name: "nat"})
	driver.RegisterConnector("lazy", func() (driverkit.Strategy, error) { return driverkit.Strategy{Name: "lazy"}, nil })
	for name, tc := range map[string]struct{ raw, driver string }{
		"unknown":    {`[{"name":"nope","module":"/x.wasm","shadow":true}]`, "nope"},
		"connector":  {`[{"name":"lazy","module":"/x.wasm","shadow":true}]`, "lazy"},
		"moduleless": {`[{"name":"nat","shadow":true}]`, "nat"},
	} {
		if err := driverrows.Use(json.RawMessage(tc.raw)); err != nil {
			t.Errorf("%s: Use = %v, want the shadow skipped", name, err)
		}
		if reason, ok := driver.ShadowSkipped(tc.driver); !ok || reason == "" {
			t.Errorf("%s: ShadowSkipped = %q, %v, want a reason", name, reason, ok)
		}
		if _, ok := driver.ShadowModule(tc.driver); ok {
			t.Errorf("%s: a skipped row left a shadow behind", name)
		}
	}
	if driver.Has("nope") {
		t.Error("a skipped shadow row registered a Driver")
	}
}

// A shadow row is never worth failing the run over: one that disagrees with the
// native Descriptor or repeats a shadow row is skipped with a reason.
func TestMalformedShadowRowsAreSkippedNotFatal(t *testing.T) {
	for name, raw := range map[string]string{
		"descriptor": `[{"name":"nat","module":"/x.wasm","shadow":true,"providerNamespacedModels":true}]`,
		"duplicate":  `[{"name":"nat","module":"/x.wasm","shadow":true},{"name":"nat","module":"/y.wasm","shadow":true}]`,
	} {
		t.Run(name, func(t *testing.T) {
			drivertest.Isolate(t)
			driver.Register(driverkit.Strategy{Name: "nat"})
			driver.UseDescriptor(driver.Descriptor{Name: "nat"})
			if err := driverrows.Use(json.RawMessage(raw)); err != nil {
				t.Errorf("Use = %v, want the shadow skipped", err)
			}
			if reason, ok := driver.ShadowSkipped("nat"); !ok || reason == "" {
				t.Errorf("ShadowSkipped = %q, %v, want a reason", reason, ok)
			}
			if _, ok := driver.ShadowModule("nat"); ok {
				t.Error("a skipped row left a shadow behind")
			}
		})
	}
}

func TestShadowRowBesideNativeRowOfSameNameIsFine(t *testing.T) {
	drivertest.Isolate(t)
	driver.Register(driverkit.Strategy{Name: "nat"})
	driver.UseDescriptor(driver.Descriptor{Name: "nat"})
	if err := driverrows.Use(json.RawMessage(`[{"name":"nat"},{"name":"nat","module":"/m.wasm","shadow":true}]`)); err != nil {
		t.Fatal(err)
	}
	if got, ok := driver.ShadowModule("nat"); !ok || got != "/m.wasm" {
		t.Errorf("ShadowModule(nat) = %q, %v, want /m.wasm", got, ok)
	}
}

// useFile applies raw through the Box's entry point, the one that gates on the
// first-party mark.
func useFile(t *testing.T, raw json.RawMessage) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rows.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return driverrows.UseFile(path)
}

func TestBoxRefusesToLoadConnectorDriverNotFirstParty(t *testing.T) {
	drivertest.Isolate(t)
	if err := useFile(t, rows(t, []map[string]any{{"name": "outsider", "module": "/m/outsider.wasm"}})); err != nil {
		t.Fatalf("UseFile = %v, want the unmarked row registered, not refused at startup", err)
	}
	if !driver.Has("outsider") {
		t.Fatal("UseFile registered no Driver for the unmarked row")
	}
	_, err := driver.New("outsider")
	if !errors.Is(err, driverrows.ErrNotFirstParty) || !strings.Contains(err.Error(), "outsider") {
		t.Fatalf("New(outsider) = %v, want ErrNotFirstParty naming outsider", err)
	}
}

func TestBoxUnmarkedRowDoesNotBlockOtherDrivers(t *testing.T) {
	drivertest.Isolate(t)
	driver.Register(driverkit.Strategy{Name: "nat"})
	driver.RegisterConnector("trusted", func() (driverkit.Strategy, error) { return driverkit.Strategy{Name: "trusted"}, nil })
	err := useFile(t, rows(t, []map[string]any{
		{"name": "outsider", "module": "/m/x.wasm"},
		{"name": "nat"},
	}))
	if err != nil {
		t.Fatalf("UseFile = %v", err)
	}
	for _, name := range []string{"nat", "trusted"} {
		if _, err := driver.New(name); err != nil {
			t.Errorf("New(%s) = %v, want it unaffected by the unmarked row", name, err)
		}
	}
}

func TestBoxLoadsFirstPartyConnectorDriver(t *testing.T) {
	drivertest.Isolate(t)
	wasm := fixtureWasm(t, "driver.wasm")
	if err := useFile(t, rows(t, []map[string]any{{"name": "fixture", "module": wasm, "firstParty": true}})); err != nil {
		t.Fatal(err)
	}
	d, err := driver.New("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if ev, err := d.ResultEvent("hi"); err != nil || string(ev) != "result hi\n" {
		t.Errorf("ResultEvent = %q, %v", ev, err)
	}
}

func TestBoxAppliesFirstPartyShadowRow(t *testing.T) {
	drivertest.Isolate(t)
	driver.Register(driverkit.Strategy{Name: "claude"})
	if err := useFile(t, claudeShadowRow(t, true)); err != nil {
		t.Fatal(err)
	}
	if got, ok := driver.ShadowModule("claude"); !ok || got != "/m/claude.wasm" {
		t.Errorf("ShadowModule(claude) = %q, %v, want /m/claude.wasm", got, ok)
	}
}

func TestBoxSkipsShadowRowNotFirstParty(t *testing.T) {
	drivertest.Isolate(t)
	driver.Register(driverkit.Strategy{Name: "claude"})
	if err := useFile(t, claudeShadowRow(t, false)); err != nil {
		t.Fatalf("UseFile = %v, want the shadow skipped, not fatal", err)
	}
	reason, ok := driver.ShadowSkipped("claude")
	if !ok || !strings.Contains(reason, driverrows.ErrNotFirstParty.Error()) {
		t.Errorf("ShadowSkipped = %q, %v, want the not-first-party reason", reason, ok)
	}
	if _, ok := driver.ShadowModule("claude"); ok {
		t.Error("a skipped row left a shadow behind")
	}
}

func TestHostUseAcceptsConnectorDriverNotFirstParty(t *testing.T) {
	drivertest.Isolate(t)
	if err := driverrows.Use(rows(t, []map[string]any{{"name": "outsider", "module": "/m/outsider.wasm"}})); err != nil {
		t.Fatalf("Use = %v, want the Launcher to carry no first-party gate", err)
	}
	if !driver.Has("outsider") {
		t.Error("Use registered no Driver")
	}
}

// claudeShadowRow is claude's shadow row, carrying its generated Descriptor so
// only the first-party mark decides whether it applies.
func claudeShadowRow(t *testing.T, firstParty bool) json.RawMessage {
	t.Helper()
	desc, ok := driver.DescriptorFor("claude")
	if !ok {
		t.Fatal("claude has no Descriptor")
	}
	return rows(t, []driverrows.Row{{Descriptor: desc, Module: "/m/claude.wasm", Shadow: true, FirstParty: firstParty}})
}
