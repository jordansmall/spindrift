package driverrows_test

import (
	"encoding/json"
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
	doc := rows(t, []map[string]any{{"name": "filefixture", "module": wasm}})
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
