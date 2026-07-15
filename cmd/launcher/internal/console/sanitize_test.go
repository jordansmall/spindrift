package console

import "testing"

// TestSanitizeControlSequences_StripsOSCAndKeepsUTF8 verifies the OSC
// "set window title" sequence is stripped and a multi-byte UTF-8 rune
// adjacent to it survives untouched (#721) — a byte-range check for C1
// controls would otherwise misfire on UTF-8 continuation bytes.
func TestSanitizeControlSequences_StripsOSCAndKeepsUTF8(t *testing.T) {
	in := "café \x1b]0;pwned\x07 done"
	want := "café  done"
	if got := SanitizeControlSequences(in); got != want {
		t.Errorf("SanitizeControlSequences(%q) = %q, want %q", in, got, want)
	}
}

// TestSanitizeControlSequences_PreservesNewlineAndTab verifies structural
// whitespace survives sanitization even though it is technically a C0
// control character, so rendered transcript formatting is unaffected.
func TestSanitizeControlSequences_PreservesNewlineAndTab(t *testing.T) {
	in := "line one\n\tindented"
	if got := SanitizeControlSequences(in); got != in {
		t.Errorf("SanitizeControlSequences(%q) = %q, want unchanged", in, got)
	}
}
