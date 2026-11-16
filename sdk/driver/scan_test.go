package driver

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func scanOK(t *testing.T, line string) Value {
	t.Helper()
	v, ok := Scan(line)
	if !ok {
		t.Fatalf("Scan(%q) not ok", line)
	}
	return v
}

func TestScanValidityMatchesEncodingJSON(t *testing.T) {
	deep := func(n int) string { return strings.Repeat("[", n) + strings.Repeat("]", n) }
	for _, line := range []string{
		``, ` `, `null`, ` true `, `false`, `nul`, `nulll`, `truee`, `True`, `{}`, `[]`, `{ }`, `[ ]`, `{"a":1}`,
		`{"a":1,}`, `[1,]`, `[,1]`, `{"a"}`, `{"a":}`, `{:1}`, `{1:1}`, `{"a":1 "b":2}`, `[1 2]`, `{"a":1]`, `[1}`,
		`{]`, `[}`, `{"a":1}x`, `{"a":1} {"b":2}`, "\t\n\r {\"a\":[1,2,{\"b\":null}]}\n", "\v1", "1\f",
		`0`, `-0`, `-`, `01`, `-01`, `1.`, `.5`, `1.5`, `1e5`, `1E+5`, `1e-5`, `1e`, `1e+`, `+1`, `0x1`, `1.5e`,
		`"a"`, `"a`, `"a\"`, `"é"`, `"\u00g9"`, `"\u00e"`, `"😀"`, `"\ud83d"`, `"\ude00"`, `"\ud83dA"`,
		`"\x"`, `"\'"`, `"\/"`, "\"a\x01\"", "\"a\tb\"", "\"\xff\xfe\"", "\"\xe2\x82\"", `"é"`,
		deep(10000), deep(10001), strings.Repeat("{\"a\":", 10000) + "1" + strings.Repeat("}", 10000),
		strings.Repeat("{\"a\":", 10001) + "1" + strings.Repeat("}", 10001),
		strings.Repeat("[", 10001), `[[[`, `{"a":[1,{"b":[true,false,null]}],"c":"d"}`,
	} {
		_, ok := Scan(line)
		if want := json.Valid([]byte(line)); ok != want {
			t.Errorf("Scan(%.40q) ok = %v, json.Valid = %v", line, ok, want)
		}
	}
}

func TestScanTapeWalk(t *testing.T) {
	line := ` {"a":[1,"x",{"b":null}],"c":true,"a":{},"d":[]} `
	root := scanOK(t, strings.TrimSpace(line))
	if root.Kind() != Object {
		t.Fatalf("kind = %v", root.Kind())
	}
	var keys, raws []string
	for k, v := range root.Members() {
		keys = append(keys, k)
		raws = append(raws, v.Raw())
	}
	if want := []string{"a", "c", "a", "d"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %q, want %q", keys, want)
	}
	if want := []string{`[1,"x",{"b":null}]`, `true`, `{}`, `[]`}; !reflect.DeepEqual(raws, want) {
		t.Errorf("raws = %q, want %q", raws, want)
	}
	var first Value
	for _, v := range root.Members() {
		first = v
		break
	}
	var elems []string
	var kinds []Kind
	for e := range first.Elems() {
		elems = append(elems, e.Raw())
		kinds = append(kinds, e.Kind())
	}
	if want := []string{`1`, `"x"`, `{"b":null}`}; !reflect.DeepEqual(elems, want) {
		t.Errorf("elems = %q, want %q", elems, want)
	}
	if want := []Kind{Number, String, Object}; !reflect.DeepEqual(kinds, want) {
		t.Errorf("kinds = %v, want %v", kinds, want)
	}
	for range first.Members() {
		t.Error("Members of an array yielded")
	}
	for range root.Elems() {
		t.Error("Elems of an object yielded")
	}
}

func TestScanFailedValueIsInert(t *testing.T) {
	v, ok := Scan(`{`)
	if ok || v.Kind() != Invalid || v.Raw() != "" {
		t.Errorf("failed scan = (%v, %q, %v)", v.Kind(), v.Raw(), ok)
	}
	for range v.Members() {
		t.Error("Members yielded")
	}
	var s string
	if v.DecodeString(&s) {
		t.Error("DecodeString of an invalid Value reported true")
	}
}

func TestScanKeysAreUnescaped(t *testing.T) {
	root := scanOK(t, `{"a\nb":1,"\u00e9":2,"\ud83d\ude00":3,"\ud83d":4,"\ud83d\u0041":5,"x`+"\xff"+`y":6}`)
	var keys []string
	for k := range root.Members() {
		keys = append(keys, k)
	}
	want := []string{"a\nb", "é", "😀", "�", "�A", "x�y"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %q, want %q", keys, want)
	}
}

func TestScanStringWithoutEscapesIsNotCopied(t *testing.T) {
	line := `{"k":"plain é text"}`
	root := scanOK(t, line)
	for _, v := range root.Members() {
		var s string
		if !v.DecodeString(&s) || s != "plain é text" {
			t.Fatalf("DecodeString = %q", s)
		}
		if n := testing.AllocsPerRun(10, func() { v.DecodeString(&s) }); n != 0 {
			t.Errorf("allocs = %v, want 0", n)
		}
	}
}

func TestDecodeSentinels(t *testing.T) {
	check := func(raw string) {
		v := scanOK(t, raw)
		s, sWant := "sentinel", "sentinel"
		okS, errS := v.DecodeString(&s), json.Unmarshal([]byte(raw), &sWant) == nil
		if okS != errS || s != sWant {
			t.Errorf("%s string: got (%q,%v) want (%q,%v)", raw, s, okS, sWant, errS)
		}
		b, bWant := true, true
		okB, errB := v.DecodeBool(&b), json.Unmarshal([]byte(raw), &bWant) == nil
		if okB != errB || b != bWant {
			t.Errorf("%s bool: got (%v,%v) want (%v,%v)", raw, b, okB, bWant, errB)
		}
		n, nWant := 7, 7
		okN, errN := v.DecodeInt(&n), json.Unmarshal([]byte(raw), &nWant) == nil
		if okN != errN || n != nWant {
			t.Errorf("%s int: got (%v,%v) want (%v,%v)", raw, n, okN, nWant, errN)
		}
		f, fWant := 7.5, 7.5
		okF, errF := v.DecodeFloat(&f), json.Unmarshal([]byte(raw), &fWant) == nil
		if okF != errF || f != fWant {
			t.Errorf("%s float: got (%v,%v) want (%v,%v)", raw, f, okF, fWant, errF)
		}
	}
	for _, raw := range []string{
		`null`, `true`, `false`, `"s"`, `"😀"`, `""`, `0`, `-0`, `42`, `1.0`, `1e2`, `1.5`, `-7`,
		`9223372036854775807`, `9223372036854775808`, `-9223372036854775808`, `-9223372036854775809`,
		`1e400`, `-1e400`, `1e-400`, `1.7976931348623157e308`, `[]`, `{}`,
	} {
		check(raw)
	}
	if math.MaxInt != math.MaxInt64 {
		t.Skip("32-bit int")
	}
}

func TestKeyMatches(t *testing.T) {
	for _, tc := range []struct {
		key, name string
		want      bool
	}{
		{"type", "type", true},
		{"Type", "type", true},
		{"TYPE", "type", true},
		{"typ", "type", false},
		{"types", "type", false},
		{"", "type", false},
		{"tool_use_id", "tool_use_id", true},
		{"Tool_Use_ID", "tool_use_id", true},
		{"tool-use-id", "tool_use_id", false},
		{"ſ", "s", true},
		{"K", "k", true},
		{"İd", "id", false},
		{"ıd", "id", false},
		{"é", "É", true},
	} {
		if got := KeyMatches(tc.key, tc.name); got != tc.want {
			t.Errorf("KeyMatches(%q, %q) = %v, want %v", tc.key, tc.name, got, tc.want)
		}
	}
}

// scanSeeds feeds FuzzScan; the deep ones sit on encoding/json's nesting limit.
var scanSeeds = []string{
	``, `null`, `true`, `-0`, `1e400`, `"a"`, `"é😀\ud83dA\ud83d"`, "\"\xff\xe2\x82\"", "\"a\x01\"",
	`{"a":1,"A":2,"a":null,"b":{"c":[1,2.5e3,-1,"x\ny",true,false,null]}}`, `{"type":"spindrift_op","spindrift_op":null}`,
	`{"a":1,}`, `{"a":`, `[1,2`, `[1 2]`, `{"a" 1}`, `"\u12`, `01`, `1.`, `-`, `9223372036854775808`, `-9223372036854775809`,
	`{"tool_use_id":"x","İd":1,"ſ":2,"K":3}`, "\t\n {\"a\":[ ]} \r\n", `{"a":1}x`,
	strings.Repeat("[", 10000) + strings.Repeat("]", 10000), strings.Repeat("[", 10001) + strings.Repeat("]", 10001),
	strings.Repeat(`{"a":`, 10000) + `1` + strings.Repeat(`}`, 10000),
}

// rawOf is what json.Unmarshal into a json.RawMessage keeps for a value.
func rawOf(t *testing.T, v Value) string {
	t.Helper()
	var m json.RawMessage
	if err := json.Unmarshal([]byte(v.Raw()), &m); err != nil {
		t.Fatalf("Raw %q does not decode: %v", v.Raw(), err)
	}
	return string(m)
}

func checkWalk(t *testing.T, v Value) {
	t.Helper()
	raw := v.Raw()
	if raw != rawOf(t, v) {
		t.Fatalf("Raw %q differs from RawMessage", raw)
	}
	if !json.Valid([]byte(raw)) {
		t.Fatalf("Raw %q is not valid JSON", raw)
	}
	b := []byte(raw)

	var s, sWant = "sentinel", "sentinel"
	okS, errS := v.DecodeString(&s), json.Unmarshal(b, &sWant) == nil
	if okS != errS || s != sWant || !utf8.ValidString(s) {
		t.Fatalf("string %q: got (%q,%v) want (%q,%v)", raw, s, okS, sWant, errS)
	}
	bo, boWant := true, true
	okB, errB := v.DecodeBool(&bo), json.Unmarshal(b, &boWant) == nil
	if okB != errB || bo != boWant {
		t.Fatalf("bool %q: got (%v,%v) want (%v,%v)", raw, bo, okB, boWant, errB)
	}
	n, nWant := 7, 7
	okN, errN := v.DecodeInt(&n), json.Unmarshal(b, &nWant) == nil
	if okN != errN || n != nWant {
		t.Fatalf("int %q: got (%v,%v) want (%v,%v)", raw, n, okN, nWant, errN)
	}
	f, fWant := 7.5, 7.5
	okF, errF := v.DecodeFloat(&f), json.Unmarshal(b, &fWant) == nil
	if okF != errF || f != fWant {
		t.Fatalf("float %q: got (%v,%v) want (%v,%v)", raw, f, okF, fWant, errF)
	}

	switch v.Kind() {
	case Object:
		// An object's members, decoded as a map from the last copy of each
		// key, must equal what the walk sees.
		var want map[string]json.RawMessage
		if err := json.Unmarshal(b, &want); err != nil {
			t.Fatalf("object %q: %v", raw, err)
		}
		got := map[string]string{}
		for k, m := range v.Members() {
			got[k] = m.Raw()
			checkWalk(t, m)
		}
		if len(got) != len(want) {
			t.Fatalf("object %q: %d distinct keys, want %d", raw, len(got), len(want))
		}
		for k, w := range want {
			if got[k] != string(w) {
				t.Fatalf("object %q key %q: %q, want %q", raw, k, got[k], w)
			}
		}
	case Array:
		var want []json.RawMessage
		if err := json.Unmarshal(b, &want); err != nil {
			t.Fatalf("array %q: %v", raw, err)
		}
		i := 0
		for e := range v.Elems() {
			if i >= len(want) || e.Raw() != string(want[i]) {
				t.Fatalf("array %q element %d mismatch", raw, i)
			}
			i++
			checkWalk(t, e)
		}
		if i != len(want) {
			t.Fatalf("array %q: %d elements, want %d", raw, i, len(want))
		}
	}
}

func FuzzScan(f *testing.F) {
	for _, s := range scanSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		v, ok := Scan(line)
		if want := json.Valid([]byte(line)); ok != want {
			t.Fatalf("Scan ok = %v, json.Valid = %v", ok, want)
		}
		if !ok {
			return
		}
		if len(line) > 1<<14 {
			t.Skip("walk is quadratic on deep input")
		}
		checkWalk(t, v)
	})
}

func FuzzKeyMatches(f *testing.F) {
	for _, k := range []string{"type", "Type", "TYPE", "tool_use_id", "Tool_Use_Id", "ſ", "K", "İd", "ıd", "typ\xff", "", "é", "ſype", "tyſe"} {
		f.Add(k)
	}
	f.Fuzz(func(t *testing.T, key string) {
		for _, name := range []string{"type", "tool_use_id", "spindrift_op", "skey"} {
			// Build the object by hand: json.Marshal would escape the key,
			// and the key must reach the decoder unescaped by Scan.
			quoted, err := json.Marshal(key)
			if err != nil {
				t.Fatal(err)
			}
			doc := `{` + string(quoted) + `:"x"}`
			var viaStdlib string
			switch name {
			case "type":
				var s struct {
					T string `json:"type"`
				}
				json.Unmarshal([]byte(doc), &s)
				viaStdlib = s.T
			case "tool_use_id":
				var s struct {
					T string `json:"tool_use_id"`
				}
				json.Unmarshal([]byte(doc), &s)
				viaStdlib = s.T
			case "spindrift_op":
				var s struct {
					T string `json:"spindrift_op"`
				}
				json.Unmarshal([]byte(doc), &s)
				viaStdlib = s.T
			case "skey":
				var s struct {
					T string `json:"skey"`
				}
				json.Unmarshal([]byte(doc), &s)
				viaStdlib = s.T
			}
			root, ok := Scan(doc)
			if !ok {
				t.Fatalf("Scan(%q) not ok", doc)
			}
			got := false
			for k := range root.Members() {
				got = KeyMatches(k, name)
			}
			if got != (viaStdlib == "x") {
				t.Fatalf("key %q name %q: KeyMatches = %v, encoding/json matched = %v", key, name, got, viaStdlib == "x")
			}
		}
	})
}
