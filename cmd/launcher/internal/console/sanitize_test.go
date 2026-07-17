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

// TestSanitizeControlSequences_LeaksNonCSIOSCBodyButStripsESC documents the
// deliberate scope noted at sanitize.go's ESC case (#1018): a DCS
// introducer's body and terminator framing leak as visible text, but every
// raw ESC byte is still stripped so no sequence reaches the terminal.
func TestSanitizeControlSequences_LeaksNonCSIOSCBodyButStripsESC(t *testing.T) {
	in := "before\x1bPmalicious\x1b\\after"
	want := "beforePmalicious\\after"
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

// TestSanitizeControlSequences_RawC1SurvivesAsReplacementChar verifies a
// raw invalid C1 byte (0x9b, 0x9d) decodes as U+FFFD, which is outside
// the 0x80-0x9f range check, so it survives sanitization rather than
// being stripped (#1019) — documented, harmless behavior, not a strip
// bug.
func TestSanitizeControlSequences_RawC1SurvivesAsReplacementChar(t *testing.T) {
	for _, raw := range []byte{0x9b, 0x9d} {
		in := string([]byte{'a', raw, 'b'})
		want := "a�b"
		if got := SanitizeControlSequences(in); got != want {
			t.Errorf("SanitizeControlSequences(%q) = %q, want %q", in, got, want)
		}
	}
}
