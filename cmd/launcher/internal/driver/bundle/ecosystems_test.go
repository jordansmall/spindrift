package bundle_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystem/cargo"
	"spindrift.dev/launcher/internal/ecosystem/gomod"
	"spindrift.dev/launcher/internal/ecosystem/gradle"
	"spindrift.dev/launcher/internal/registryroutes"
	"spindrift.dev/launcher/internal/registryvocab"
)

// carryOverClause is the refusal the error must name for a retired key whose
// value its real owner's replacement key rejects.
func carryOverClause(retiredKey, owner, replacement, rest string) string {
	return fmt.Sprintf("%s cannot carry over, so the stanza omits it: %s %s",
		retiredKey, registryvocab.RouteDeclarationKeyLabel(owner, replacement), rest)
}

// registryroutes keeps the retired routes-file keys as frozen vocabulary and
// panics on one that resolves to no Strategy. Core tests cannot link the real
// registration, so this is where that guarantee is held.
func TestEveryRetiredRouteKeyResolvesToARegisteredStrategy(t *testing.T) {
	registered := ecosystem.Registered()
	if len(registered) == 0 {
		t.Fatal("the Bundle registered no ecosystem Strategies")
	}
	for key, owner := range map[string]ecosystem.Strategy{
		ecosystem.GradleRetiredRouteKey: gradle.Strategy,
		ecosystem.GoRetiredRouteKey:     gomod.Strategy,
		ecosystem.CargoRetiredRouteKey:  cargo.Strategy,
	} {
		got, ok := ecosystem.RowByRetiredRouteKey(key, ecosystem.Registered())
		if !ok {
			t.Errorf("retired key %q resolves to no registered Strategy", key)
			continue
		}
		if got.Name != owner.Name {
			t.Errorf("retired key %q resolves to %q, want %q", key, got.Name, owner.Name)
		}
		if !slices.ContainsFunc(registered, func(s ecosystem.Strategy) bool { return s.Name == got.Name }) {
			t.Errorf("retired key %q resolves to %q, which Registered() lacks", key, got.Name)
		}
	}
}

// The real owners' own validators decide whether a retired value carries over;
// registryroutes' tests pin the mechanics with stand-ins, this pins the wording
// an operator sees.
func TestRetiredRouteInvalidValueNamesTheRealOwnersValidation(t *testing.T) {
	const base = "\n[[routes]]\nmatch-host = \"repo.example.com\"\ncredential = { netrc = \"~/.netrc\" }\n"
	for _, tc := range []struct {
		name    string
		decl    string
		invalid string
		clause  string
	}{
		{"go-relative", ecosystem.GoRetiredRouteKey + ` = "mirror"`, "mirror",
			carryOverClause(ecosystem.GoRetiredRouteKey, gomod.Strategy.Name, "path", `"mirror" must start with "/"`)},
		{"gradle-dotdot", ecosystem.GradleRetiredRouteKey + ` = "/a/../b"`, "/a/../b",
			carryOverClause(ecosystem.GradleRetiredRouteKey, gradle.Strategy.Name, "path", `"/a/../b" must not contain a ".." segment`)},
		{"cargo-bad-name", ecosystem.CargoRetiredRouteKey + ` = ["evil; rm"]`, "evil; rm",
			carryOverClause(ecosystem.CargoRetiredRouteKey, cargo.Strategy.Name, "registries", `name "evil; rm" must match ^[A-Za-z0-9_-]+$`)},
		{"cargo-empty-name", ecosystem.CargoRetiredRouteKey + ` = [""]`, `""`,
			carryOverClause(ecosystem.CargoRetiredRouteKey, cargo.Strategy.Name, "registries", "names an empty string")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := registryroutes.Parse([]byte(base+tc.decl+"\n"), ecosystem.Registered())
			if err == nil {
				t.Fatal("expected a retirement error, got nil")
			}
			if !strings.Contains(err.Error(), tc.clause) {
				t.Errorf("error lacks %q, got: %v", tc.clause, err)
			}
			_, stanza, ok := strings.Cut(err.Error(), "routes-file stanza:\n\n")
			if !ok {
				t.Fatalf("expected a replacement stanza in the error, got: %v", err)
			}
			if strings.Contains(stanza, tc.invalid) {
				t.Errorf("stanza echoes the invalid value %q:\n%s", tc.invalid, stanza)
			}
			if _, err := registryroutes.Parse([]byte(stanza), ecosystem.Registered()); err != nil {
				t.Errorf("expected the replacement stanza to parse, got: %v\nstanza:\n%s", err, stanza)
			}
		})
	}
}
