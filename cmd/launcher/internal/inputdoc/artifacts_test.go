package inputdoc

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestArtifactsDecodeSelectsFamilyPart(t *testing.T) {
	cases := []struct {
		name      string
		doc       string
		wantBwrap bool
		wantOCI   bool
	}{
		{"bwrap", `{"RUNNER_KIND":"bwrap","DRIVER":"claude","PASSWD_FILE_DRV":"/nix/store/p"}`, true, false},
		{"oci", `{"RUNNER_KIND":"oci","DRIVER":"claude","IMAGE_ARCHIVE":"/nix/store/i"}`, false, true},
		{"absent kind", `{"DRIVER":"claude"}`, false, false},
		{"unknown kind", `{"RUNNER_KIND":"nope","DRIVER":"claude"}`, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var a Artifacts
			if err := json.Unmarshal([]byte(tc.doc), &a); err != nil {
				t.Fatal(err)
			}
			if got := a.Shared.Driver.String(); got != "claude" {
				t.Errorf("Shared.Driver = %q, want claude", got)
			}
			if (a.Bwrap != nil) != tc.wantBwrap || (a.OCI != nil) != tc.wantOCI {
				t.Fatalf("family parts: bwrap=%v oci=%v, want bwrap=%v oci=%v", a.Bwrap != nil, a.OCI != nil, tc.wantBwrap, tc.wantOCI)
			}
			if tc.wantBwrap {
				if v, ok := a.Bwrap.PasswdFileDrv.Lookup(); !ok || v != "/nix/store/p" {
					t.Errorf("Bwrap.PasswdFileDrv = %q, %v", v, ok)
				}
			}
			if tc.wantOCI {
				if v, ok := a.OCI.ImageArchive.Lookup(); !ok || v != "/nix/store/i" {
					t.Errorf("OCI.ImageArchive = %q, %v", v, ok)
				}
			}
		})
	}
}

func TestValueLookupDistinguishesAbsentFromEmpty(t *testing.T) {
	var a Artifacts
	if err := json.Unmarshal([]byte(`{"RUNNER_KIND":"bwrap","DRIVER_SESSION_CACHE_DIR":""}`), &a); err != nil {
		t.Fatal(err)
	}
	if v, ok := a.Shared.DriverSessionCacheDir.Lookup(); !ok || v != "" {
		t.Errorf("empty-valued key: Lookup = %q, %v; want \"\", true", v, ok)
	}
	if _, ok := a.Shared.ChoreCatalog.Lookup(); ok {
		t.Error("absent key: Lookup ok = true, want false")
	}
	if !a.Shared.ChoreCatalog.IsZero() || a.Shared.DriverSessionCacheDir.IsZero() {
		t.Error("IsZero must be true only for the absent key")
	}
}

func TestFlagAccessors(t *testing.T) {
	var a Artifacts
	if err := json.Unmarshal([]byte(`{"RUNNER_KIND":"oci","FULLY_LOCAL":"true","FILER_ENABLED":"false"}`), &a); err != nil {
		t.Fatal(err)
	}
	if !a.Shared.FullyLocal.Bool() || a.Shared.FilerEnabled.Bool() {
		t.Errorf("Bool: FullyLocal=%v FilerEnabled=%v, want true false", a.Shared.FullyLocal.Bool(), a.Shared.FilerEnabled.Bool())
	}
	if b, ok := a.Shared.FilerEnabled.LookupBool(); !ok || b {
		t.Errorf("LookupBool(false key) = %v, %v; want false, true", b, ok)
	}
	if b, ok := a.Shared.WorkerProvisioned.LookupBool(); ok || b {
		t.Errorf("LookupBool(absent key) = %v, %v; want false, false", b, ok)
	}
	if got := FlagOf(true); !got.Bool() {
		t.Error("FlagOf(true).Bool() = false")
	}
}

func TestArtifactsRoundTrip(t *testing.T) {
	for _, doc := range []string{
		`{"RUNNER_KIND":"bwrap","DRIVER":"claude","FULLY_LOCAL":"true","PASSWD_FILE_DRV":"p","NIX_STORE_WRITABLE":"false","IMAGE_TAG":"t"}`,
		`{"RUNNER_KIND":"oci","DRIVER":"claude","IMAGE_ARCHIVE":"i","IMAGE_TAG":"t"}`,
	} {
		var a Artifacts
		if err := json.Unmarshal([]byte(doc), &a); err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		var want, got map[string]string
		if err := json.Unmarshal([]byte(doc), &want); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("marshal = %s, want the keys of %s", out, doc)
		}
		var back Artifacts
		if err := json.Unmarshal(out, &back); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(back, a) {
			t.Errorf("round trip changed the document: %+v vs %+v", back, a)
		}
	}
}

func TestArtifactKeyTables(t *testing.T) {
	if len(SharedArtifactKeys) == 0 {
		t.Fatal("SharedArtifactKeys is empty")
	}
	for _, kind := range []string{"bwrap", "oci"} {
		if len(FamilyArtifactKeys[kind]) == 0 {
			t.Errorf("FamilyArtifactKeys[%q] is empty", kind)
		}
	}
	// Every shared key is reachable through the Shared struct: a document
	// carrying all of them decodes with none left unset.
	doc := map[string]string{"RUNNER_KIND": "oci"}
	for _, k := range SharedArtifactKeys {
		if _, ok := doc[k]; !ok {
			doc[k] = "v"
		}
	}
	b, _ := json.Marshal(doc)
	var a Artifacts
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(a.Shared)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range SharedArtifactKeys {
		if _, ok := got[k]; !ok {
			t.Errorf("shared key %s lost in decode/encode", k)
		}
	}
}

func TestArtifactsDecodeRejectsKeyOutsideKindFamily(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{"absent kind, family key", `{"DRIVER":"claude","IMAGE_TAG":"t"}`, `artifact "IMAGE_TAG" is not carried by RUNNER_KIND ""`},
		{"unknown kind, family key", `{"RUNNER_KIND":"nope","IMAGE_TAG":"t"}`, `artifact "IMAGE_TAG" is not carried by RUNNER_KIND "nope"`},
		{"oci, bwrap key", `{"RUNNER_KIND":"oci","PASSWD_FILE":"p"}`, `artifact "PASSWD_FILE" is not carried by RUNNER_KIND "oci"`},
		{"bwrap, oci key", `{"RUNNER_KIND":"bwrap","IMAGE_ARCHIVE":"i"}`, `artifact "IMAGE_ARCHIVE" is not carried by RUNNER_KIND "bwrap"`},
		{"bwrap, own key", `{"RUNNER_KIND":"bwrap","PASSWD_FILE":"p"}`, ""},
		{"oci, key both families carry", `{"RUNNER_KIND":"oci","IMAGE_TAG":"t"}`, ""},
		{"unknown key tolerated", `{"RUNNER_KIND":"oci","FUTURE_KEY":"x"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var a Artifacts
			err := json.Unmarshal([]byte(tc.doc), &a)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error = %v, want it to contain %s", err, tc.wantErr)
			}
		})
	}
}
