package registryroutes

import (
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
)

// mergeRetiredRouteEcosystems panics on a key that resolves to no
// ecosystem.Table row, so this test is what keeps that panic unreachable: only
// a row dropping its RetiredRouteKey out from under retiredRouteKeys could
// trip it.
func TestRetiredRouteKeysResolveToRows(t *testing.T) {
	for _, entry := range retiredRouteKeys {
		if _, ok := ecosystem.RowByRetiredRouteKey(entry.key); !ok {
			t.Errorf("retiredRouteKeys key %q does not resolve to any ecosystem.Table row", entry.key)
		}
	}
}

// rawRouteFieldsByTag indexes rawRoute's fields by toml tag.
func rawRouteFieldsByTag() map[string]reflect.StructField {
	byTag := map[string]reflect.StructField{}
	rt := reflect.TypeOf(rawRoute{})
	for i := range rt.NumField() {
		byTag[rt.Field(i).Tag.Get("toml")] = rt.Field(i)
	}
	return byTag
}

// retiredKeyFields maps each retired key to the rawRoute field whose toml tag
// spells it.
func retiredKeyFields(t *testing.T) map[string]reflect.StructField {
	t.Helper()
	byTag := rawRouteFieldsByTag()
	fields := map[string]reflect.StructField{}
	for _, entry := range retiredRouteKeys {
		field, ok := byTag[entry.key]
		if !ok {
			t.Fatalf("rawRoute has no field with toml tag %q", entry.key)
		}
		fields[entry.key] = field
	}
	return fields
}

// A Go struct tag cannot reference a const, so the retired keys' spelling in
// retiredRouteKeys and in rawRoute's toml tags can drift apart. A drifted tag
// would send the key to the strict decoder's unknown-key error instead of the
// retirement remedy.
func TestRawRouteHasAFieldForEveryRetiredRouteKey(t *testing.T) {
	byTag := rawRouteFieldsByTag()
	for _, entry := range retiredRouteKeys {
		if _, ok := byTag[entry.key]; !ok {
			t.Errorf("rawRoute has no field with toml tag %q", entry.key)
		}
	}
}

// retiredDeclaration spells key as a TOML line whose value fits the field it
// decodes into, either populated or empty.
func retiredDeclaration(t *testing.T, key string, field reflect.StructField, empty bool) string {
	t.Helper()
	switch field.Type {
	case reflect.TypeOf((*string)(nil)):
		if empty {
			return key + ` = ""`
		}
		return key + ` = "/x"`
	case reflect.TypeOf((*[]string)(nil)):
		if empty {
			return key + ` = []`
		}
		return key + ` = ["x"]`
	}
	t.Fatalf("rawRoute field %s has type %s, which retiredDeclaration cannot spell", field.Name, field.Type)
	return ""
}

const retiredBaseRoute = `
[[routes]]
match-host = "repo.example.com"
credential = { netrc = "~/.netrc" }
`

// The retirement gate runs before any row is consulted, so a retired key is
// refused as retired even when no row exists to look it up in, and detection is
// by presence: an empty value is as retired as a populated one.
func TestRetiredRouteKeysFireBeforeRowsAreConsulted(t *testing.T) {
	for key, field := range retiredKeyFields(t) {
		for _, empty := range []bool{false, true} {
			doc := retiredBaseRoute + retiredDeclaration(t, key, field, empty) + "\n"
			_, err := ParseRows([]byte(doc), nil)
			if err == nil {
				t.Errorf("%s (empty=%v): expected a retirement error, got nil", key, empty)
				continue
			}
			if want := key + " is retired"; !strings.Contains(err.Error(), want) {
				t.Errorf("%s (empty=%v): error lacks %q, got: %v", key, empty, want, err)
			}
		}
	}
}

// Every retired key beside the two ADR 0047 keys yields one error naming them
// all with both ADRs' rationale, not separate rejections a caller could only
// observe one at a time.
func TestRetiredRouteKeysAcrossBothADRsNameAllInOneError(t *testing.T) {
	doc := retiredBaseRoute + "upstream-base-url = \"https://repo.example.com/x\"\nenforce-allowlist = true\n"
	wants := []string{"upstream-base-url", "enforce-allowlist", "ADR 0047", "#3261", "ADR 0048", "#3405"}
	for key, field := range retiredKeyFields(t) {
		doc += retiredDeclaration(t, key, field, false) + "\n"
		wants = append(wants, key)
	}
	_, err := ParseRows([]byte(doc), nil)
	if err == nil {
		t.Fatal("expected a retirement error, got nil")
	}
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to contain %q, got: %v", want, err)
		}
	}
}

// A retired key renders as its owning row's block, after the credential line,
// beside whatever block the route declared itself. The base route has no allow,
// so credential is the last top-level key the stanza renders.
func TestRetiredRouteKeyStanzaCarriesRowBlockBesideDeclaredBlock(t *testing.T) {
	for key, field := range retiredKeyFields(t) {
		row, ok := ecosystem.RowByRetiredRouteKey(key)
		if !ok {
			t.Errorf("%s: no ecosystem.Table row owns the retired key", key)
			continue
		}
		doc := retiredBaseRoute + retiredDeclaration(t, key, field, false) + "\n\n[routes.ecosystems.unlisted]\nwidget = \"gizmo\"\n"
		_, err := ParseRows([]byte(doc), nil)
		if err == nil {
			t.Errorf("%s: expected a retirement error, got nil", key)
			continue
		}
		msg := err.Error()
		credential := strings.Index(msg, "credential = ")
		if credential < 0 {
			t.Errorf("%s: expected the stanza to carry a credential line, got: %v", key, err)
			continue
		}
		for _, header := range []string{"[routes.ecosystems." + row.Name + "]", "[routes.ecosystems.unlisted]"} {
			if at := strings.Index(msg, header); at < 0 || at < credential {
				t.Errorf("%s: expected %s after the credential line, got: %v", key, header, err)
			}
		}
	}
}

// The stanza an operator is told to paste must itself parse, and must carry a
// block for every row whose retired key the route declared.
func TestRetiredRouteStanzaRoundTripsThroughParse(t *testing.T) {
	doc := retiredBaseRoute
	for key, field := range retiredKeyFields(t) {
		doc += retiredDeclaration(t, key, field, false) + "\n"
	}
	_, err := ParseRows([]byte(doc), nil)
	if err == nil {
		t.Fatal("expected a retirement error, got nil")
	}
	at := strings.Index(err.Error(), "[[routes]]")
	if at < 0 {
		t.Fatalf("expected a replacement stanza in the error, got: %v", err)
	}
	stanza := err.Error()[at:]

	routes, err := Parse([]byte(stanza))
	if err != nil {
		t.Fatalf("expected the replacement stanza to parse, got: %v\nstanza:\n%s", err, stanza)
	}
	for key := range retiredKeyFields(t) {
		row, ok := ecosystem.RowByRetiredRouteKey(key)
		if !ok {
			t.Errorf("%s: no ecosystem.Table row owns the retired key", key)
			continue
		}
		if _, ok := routes[0].Ecosystems[row.Name]; !ok {
			t.Errorf("%s: expected a [routes.ecosystems.%s] block in the parsed stanza, got: %v", key, row.Name, routes[0].Ecosystems)
		}
	}
}

// A retired key on a route with a malformed credential fails on the credential,
// as ADR 0047's keys do: parseCredential runs before the retired-key check, so
// no stanza prints, the retired key is not named, and a secret-shaped value
// never reaches the error (issue #3436).
func TestRetiredRouteKeyWithMalformedCredentialFailsOnCredential(t *testing.T) {
	const secret = "ghp_supersecret"
	for _, cc := range []struct {
		name       string
		credential string
		want       string
		secret     string
	}{
		{"unknown-only", `{ bogus = "y" }`, `credential has unknown key "bogus"`, ""},
		{"unknown-secret-value", `{ token = "` + secret + `" }`, `credential has unknown key "token"`, secret},
	} {
		for key, field := range retiredKeyFields(t) {
			t.Run(cc.name+"/"+key, func(t *testing.T) {
				doc := "\n[[routes]]\nmatch-host = \"repo.example.com\"\ncredential = " + cc.credential + "\n" +
					retiredDeclaration(t, key, field, false) + "\n"
				_, err := ParseRows([]byte(doc), nil)
				if err == nil {
					t.Fatal("expected error for malformed credential, got nil")
				}
				msg := err.Error()
				if !strings.Contains(msg, cc.want) {
					t.Errorf("error = %v, want substring %q", err, cc.want)
				}
				if strings.Contains(msg, "[[routes]]") {
					t.Errorf("error prints a replacement stanza for a malformed credential, got: %v", err)
				}
				if strings.Contains(msg, key) {
					t.Errorf("error names retired key %q, want parseCredential's error to win, got: %v", key, err)
				}
				if cc.secret != "" && strings.Contains(msg, cc.secret) {
					t.Errorf("error echoes secret value %q, got: %v", cc.secret, err)
				}
			})
		}
	}
}
