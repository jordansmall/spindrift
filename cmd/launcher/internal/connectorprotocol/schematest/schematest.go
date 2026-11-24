// Package schematest validates JSON against protocol.schema.json, for tests
// only: no JSON Schema validator is a dependency of the launcher, so this
// implements exactly the keywords lib/connector-protocol/render.nix emits and
// refuses any other, which keeps a schema that grows a keyword from being
// silently under-checked. Only _test.go files import it.
package schematest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorprotocol"
)

// Keywords are the schema keywords a node may carry, each implemented below.
// "description" is an annotation and checks nothing.
var Keywords = []string{
	"$ref", "additionalProperties", "description", "enum", "items", "oneOf",
	"properties", "required", "type",
}

// Schema is the parsed protocol schema.
type Schema struct {
	defs    map[string]any
	methods map[string]method
}

type method struct{ params, result any }

type document struct {
	Defs    map[string]any `json:"$defs"`
	Methods map[string]struct {
		Params any `json:"params"`
		Result any `json:"result"`
	} `json:"methods"`
}

func decode(raw []byte) (document, error) {
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return doc, fmt.Errorf("protocol.schema.json: %w", err)
	}
	return doc, nil
}

// Load parses the embedded protocol.schema.json.
func Load() (*Schema, error) { return Parse(connectorprotocol.Schema) }

// Parse parses a protocol schema, refusing one the validator cannot judge
// faithfully: an unknown keyword or a non-string enum value.
func Parse(raw []byte) (*Schema, error) {
	doc, err := decode(raw)
	if err != nil {
		return nil, err
	}
	if bad := doc.unsupported(); len(bad) > 0 {
		return nil, fmt.Errorf("protocol.schema.json: unsupported: %s", strings.Join(bad, "; "))
	}
	s := &Schema{defs: doc.Defs, methods: map[string]method{}}
	for n, m := range doc.Methods {
		s.methods[n] = method{m.Params, m.Result}
	}
	return s, nil
}

// Methods lists the methods the schema describes, sorted.
func (s *Schema) Methods() []string {
	var names []string
	for n := range s.methods {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ValidateParams checks params, any value that marshals to JSON, against the
// method's params schema.
func (s *Schema) ValidateParams(name string, params any) error {
	return s.validate(name, params, func(m method) any { return m.params })
}

// ValidateResult is ValidateParams for the method's result.
func (s *Schema) ValidateResult(name string, result any) error {
	return s.validate(name, result, func(m method) any { return m.result })
}

func (s *Schema) validate(name string, v any, pick func(method) any) error {
	m, ok := s.methods[name]
	if !ok {
		return fmt.Errorf("method %q is not in the schema", name)
	}
	var raw []byte
	switch b := v.(type) {
	case json.RawMessage:
		raw = b
	case []byte:
		raw = b
	case string:
		// A reply written as a JSON literal in a test is a string of JSON.
		raw = []byte(b)
	default:
		var err error
		if raw, err = json.Marshal(v); err != nil {
			return err
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var val any
	if err := dec.Decode(&val); err != nil {
		return fmt.Errorf("%s: not JSON: %w", name, err)
	}
	if err := s.check(pick(m), val, "$"); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// ValidateRawParams checks a raw method's payload, which the schema types as a
// string, against its params schema.
func (s *Schema) ValidateRawParams(name string, payload string) error {
	m, ok := s.methods[name]
	if !ok {
		return fmt.Errorf("method %q is not in the schema", name)
	}
	return s.check(m.params, payload, "$")
}

func (s *Schema) check(node, v any, path string) error {
	n, ok := node.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: schema node is %T, want an object", path, node)
	}
	for k := range n {
		if !slices.Contains(Keywords, k) {
			return fmt.Errorf("%s: unsupported schema keyword %q", path, k)
		}
	}
	if ref, ok := n["$ref"].(string); ok {
		name, found := strings.CutPrefix(ref, "#/$defs/")
		def, defined := s.defs[name]
		if !found || !defined {
			return fmt.Errorf("%s: unresolvable $ref %q", path, ref)
		}
		if err := s.check(def, v, path); err != nil {
			return err
		}
	}
	if t, ok := n["type"].(string); ok {
		if err := checkType(t, v, path); err != nil {
			return err
		}
	}
	if vals, ok := n["enum"].([]any); ok && !slices.Contains(vals, v) {
		return fmt.Errorf("%s: %v is not one of %v", path, v, vals)
	}
	if alts, ok := n["oneOf"].([]any); ok {
		matched := 0
		var last error
		for _, alt := range alts {
			if err := s.check(alt, v, path); err != nil {
				last = err
			} else {
				matched++
			}
		}
		if matched != 1 {
			return fmt.Errorf("%s: matches %d of %d oneOf alternatives (last mismatch: %v)", path, matched, len(alts), last)
		}
	}
	if obj, ok := v.(map[string]any); ok {
		if err := s.checkObject(n, obj, path); err != nil {
			return err
		}
	}
	if items, ok := n["items"]; ok {
		if arr, isArr := v.([]any); isArr {
			for i, e := range arr {
				if err := s.check(items, e, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *Schema) checkObject(n map[string]any, obj map[string]any, path string) error {
	if req, ok := n["required"].([]any); ok {
		for _, r := range req {
			if _, present := obj[r.(string)]; !present {
				return fmt.Errorf("%s: missing required field %q", path, r)
			}
		}
	}
	props, _ := n["properties"].(map[string]any)
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sub := path + "." + k
		if p, ok := props[k]; ok {
			if err := s.check(p, obj[k], sub); err != nil {
				return err
			}
			continue
		}
		switch extra := n["additionalProperties"].(type) {
		case nil, bool:
			if extra == false {
				return fmt.Errorf("%s: field %q is not allowed", path, k)
			}
		default:
			if err := s.check(extra, obj[k], sub); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkType(t string, v any, path string) error {
	ok := false
	switch t {
	case "string":
		_, ok = v.(string)
	case "boolean":
		_, ok = v.(bool)
	case "null":
		ok = v == nil
	case "object":
		_, ok = v.(map[string]any)
	case "array":
		_, ok = v.([]any)
	case "number":
		_, ok = v.(json.Number)
	case "integer":
		if num, isNum := v.(json.Number); isNum {
			f, err := num.Float64()
			ok = err == nil && f == math.Trunc(f)
		}
	default:
		return fmt.Errorf("%s: unsupported type %q", path, t)
	}
	if !ok {
		return fmt.Errorf("%s: %v (%T) is not a %s", path, v, v, t)
	}
	return nil
}

// unsupported lists what the validator would silently misjudge: a keyword it
// does not implement, or an enum with a non-string value, which it could not
// match (a number decodes as json.Number, the schema's as float64). A keyword
// whose value has the wrong shape is reported too, not walked.
func (d document) unsupported() []string {
	var bad []string
	var walk func(node any, where string)
	walk = func(node any, where string) {
		n, ok := node.(map[string]any)
		if !ok {
			bad = append(bad, where+": node is not an object")
			return
		}
		for k, v := range n {
			if !slices.Contains(Keywords, k) {
				bad = append(bad, where+": "+k)
				continue
			}
			switch k {
			case "enum":
				vals, ok := v.([]any)
				if !ok {
					bad = append(bad, where+": enum is not an array")
					break
				}
				for _, e := range vals {
					if _, isStr := e.(string); !isStr {
						bad = append(bad, fmt.Sprintf("%s: enum value %v is not a string", where, e))
					}
				}
			case "properties":
				props, ok := v.(map[string]any)
				if !ok {
					bad = append(bad, where+": properties is not an object")
					break
				}
				for pn, pv := range props {
					walk(pv, where+"."+pn)
				}
			case "items":
				walk(v, where+"[]")
			case "additionalProperties":
				if _, isBool := v.(bool); !isBool {
					walk(v, where+"{}")
				}
			case "oneOf":
				alts, ok := v.([]any)
				if !ok {
					bad = append(bad, where+": oneOf is not an array")
					break
				}
				for i, alt := range alts {
					walk(alt, fmt.Sprintf("%s|%d", where, i))
				}
			}
		}
	}
	for n, def := range d.Defs {
		walk(def, "$defs."+n)
	}
	for n, m := range d.Methods {
		walk(m.Params, n+".params")
		walk(m.Result, n+".result")
	}
	sort.Strings(bad)
	return bad
}

// Call is one call a test's stub guest recorded: the method and the params
// core sent.
type Call struct {
	Method string
	Params map[string]any
}

// RequireConforms checks every recorded call's params, and the reply the stub
// gave it, against the schema. The replies are passed in because a recorded
// call holds only the params. It fails a test that recorded no calls, which
// would otherwise validate nothing.
func RequireConforms(t testing.TB, calls []Call, replies map[string]string) {
	t.Helper()
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) == 0 {
		t.Fatal("no calls recorded to validate")
	}
	for _, c := range calls {
		if err := s.ValidateParams(c.Method, c.Params); err != nil {
			t.Errorf("params core sent: %v", err)
		}
		if reply, ok := replies[c.Method]; ok {
			if err := s.ValidateResult(c.Method, reply); err != nil {
				t.Errorf("reply the guest gave: %v", err)
			}
		}
	}
}
