package npmrcvalue

import "testing"

func TestDecode(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"plain", "abc123", "abc123"},
		{"surrounding whitespace", "  abc123  ", "abc123"},
		{"trailing comment", "abc123 ; CI token", "abc123"},
		{"hash comment", "abc123#x", "abc123"},
		{"comment only hash", "#comment", ""},
		{"comment only semicolon", " ; note", ""},
		{"escaped semicolon", `a\;b`, "a;b"},
		{"escaped hash", `a\#b`, "a#b"},
		{"escaped backslash", `a\\b`, `a\b`},
		{"unknown escape kept", `a\xb`, `a\xb`},
		{"trailing backslash", `abc\`, `abc\`},
		{"single quoted", "'tok'", "tok"},
		{"double quoted", `"tok"`, "tok"},
		{"double quoted keeps semicolon", `"a;b"`, "a;b"},
		{"single quoted keeps hash", `'a#b'`, "a#b"},
		{"double quoted json escape", `"a\nb"`, "a\nb"},
		{"double quoted invalid json keeps quotes", `"bad\q"`, `"bad\q"`},
		{"single quoted keeps inner double quotes", `'a "b"'`, `a "b"`},
		{"unterminated double quote", `"half`, `"half`},
		{"lone single quote", `'`, ""},
		{"url", "https://x.example.com/", "https://x.example.com/"},
		{"url trailing comment", "https://x.example.com/ ; c", "https://x.example.com/"},
		{"url fragment cut", "https://x.example.com/#frag", "https://x.example.com/"},
		{"url escaped fragment", `https://x.example.com/\#frag`, "https://x.example.com/#frag"},
		{"url escaped semicolon", `https://x.example.com/\;a`, "https://x.example.com/;a"},
		{"double quoted url keeps comment chars", `"https://x.example.com/#a;b"`, "https://x.example.com/#a;b"},
		{"single quoted url keeps comment chars", `'https://x.example.com/#a;b'`, "https://x.example.com/#a;b"},
		{"single quoted non-json text stays", `'a\nb'`, `a\nb`},
		{"single quoted json string", `'"a\nb"'`, "a\nb"},
		{"single quoted json number stays text", `'123'`, "123"},
		{"empty single quotes", `''`, ""},
		{"empty double quotes", `""`, ""},
		{"lone double quote", `"`, `"`},
		{"mismatched quotes", `'tok"`, `'tok"`},
		{"quotes before comment are not a quoted value", `"tok" ; c`, `"tok"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decode(tc.raw); got != tc.want {
				t.Errorf("Decode(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
