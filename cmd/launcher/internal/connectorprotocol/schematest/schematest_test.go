package schematest_test

import (
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorprotocol"
	"spindrift.dev/launcher/internal/connectorprotocol/schematest"
)

func load(t *testing.T) *schematest.Schema {
	t.Helper()
	s, err := schematest.Load()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSchemaDescribesExactlyTheProtocolMethods(t *testing.T) {
	var want []string
	for name := range connectorprotocol.Methods {
		want = append(want, name)
	}
	slices.Sort(want)
	if got := load(t).Methods(); !slices.Equal(got, want) {
		t.Errorf("schema methods = %v, want %v", got, want)
	}
}

// A keyword the validator ignored would let a schema constraint pass unchecked,
// so Load refuses a schema using one; the committed schema must pass.
func TestSchemaUsesOnlyKeywordsTheValidatorImplements(t *testing.T) {
	load(t)
	_, err := schematest.Parse([]byte(`{"$defs":{},"methods":{"m":{"params":{"type":"object","pattern":"x"},"result":{"type":"string"}}}}`))
	if err == nil || !strings.Contains(err.Error(), "pattern") {
		t.Errorf("Parse error = %v, want it to refuse the pattern keyword", err)
	}
}

func TestValidatorAcceptsAWellFormedExchange(t *testing.T) {
	s := load(t)
	params := map[string]any{"target": map[string]any{"api": "https://api.example", "repo": "o/r"}, "number": "7"}
	if err := s.ValidateParams("forge.pr", params); err != nil {
		t.Errorf("params: %v", err)
	}
	if err := s.ValidateResult("forge.pr", `{"number":"7","url":"https://github.com/o/r/pull/7","state":"open","merged":false}`); err != nil {
		t.Errorf("result: %v", err)
	}
}

func TestValidatorRejects(t *testing.T) {
	s := load(t)
	target := map[string]any{"api": "https://api.example", "repo": "o/r"}
	describe := func(result string) string {
		return `{"name":"x","terminalPatterns":[],"transientPatterns":[],"writeTool":"","result":"` + result + `"}`
	}
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"wrong type": {
			s.ValidateParams("forge.pr", map[string]any{"target": target, "number": 7}),
			"is not a string",
		},
		"missing required field": {
			s.ValidateParams("forge.pr", map[string]any{"target": target}),
			`missing required field "number"`,
		},
		"extra field on a closed object": {
			s.ValidateParams("forge.pr", map[string]any{"target": target, "number": "7", "bogus": true}),
			`field "bogus" is not allowed`,
		},
		"bad enum": {
			s.ValidateResult("driver.describe", describe("bogus")),
			"is not one of",
		},
		"fault inside a $ref'd list item": {
			s.ValidateResult("driver.describe", `{"name":"x","terminalPatterns":[{"substr":1}],"transientPatterns":[],"writeTool":""}`),
			"terminalPatterns[0]",
		},
		"unknown method": {
			s.ValidateParams("forge.nope", map[string]any{}),
			"not in the schema",
		},
		"not JSON": {
			s.ValidateResult("forge.pr", `{`),
			"not JSON",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.err == nil || !strings.Contains(tc.err.Error(), tc.want) {
				t.Errorf("err = %v, want one containing %q", tc.err, tc.want)
			}
		})
	}
	// The sanity half of the enum case: the same reply with a legal value passes.
	if err := s.ValidateResult("driver.describe", describe("events")); err != nil {
		t.Errorf("legal enum value: %v", err)
	}
}

// A numeric enum would never match: the validator reads a reply's numbers as
// json.Number and the schema's as float64.
func TestParseRefusesANonStringEnum(t *testing.T) {
	const schema = `{"$defs":{},"methods":{"m":{"params":{"type":"object"},"result":{"type":"integer","enum":[1,2]}}}}`
	_, err := schematest.Parse([]byte(schema))
	if err == nil || !strings.Contains(err.Error(), "enum value 1 is not a string") {
		t.Errorf("Parse error = %v, want it to refuse the numeric enum", err)
	}
	const ok = `{"$defs":{},"methods":{"m":{"params":{"type":"object"},"result":{"type":"string","enum":["a"]}}}}`
	if _, err := schematest.Parse([]byte(ok)); err != nil {
		t.Errorf("Parse refused a string enum: %v", err)
	}
}

// A malformed keyword value is reported as unsupported, not a panic.
func TestParseRefusesAMalformedKeywordValue(t *testing.T) {
	for _, result := range []string{
		`{"type":"string","enum":"a"}`,
		`{"type":"object","properties":[]}`,
		`{"oneOf":{}}`,
	} {
		schema := `{"$defs":{},"methods":{"m":{"params":{"type":"object"},"result":` + result + `}}}`
		if _, err := schematest.Parse([]byte(schema)); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Errorf("Parse(%s) error = %v, want unsupported", result, err)
		}
	}
}
