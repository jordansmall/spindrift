package console

import "testing"

// This test pins #721: a byte-range check for C1 controls misfires on
// UTF-8 continuation bytes, so a rune next to a stripped OSC sequence
// must survive untouched.
func TestSanitizeControlSequences_StripsOSCAndKeepsUTF8(t *testing.T) {
	in := "café \x1b]0;pwned\x07 done"
	want := "café  done"
	if got := SanitizeControlSequences(in); got != want {
		t.Errorf("SanitizeControlSequences(%q) = %q, want %q", in, got, want)
	}
}

// This test pins the deliberate scope noted at sanitize.go's ESC case
// (#1018): a DCS introducer's body and terminator framing leak as visible
// text, but every raw ESC byte is still stripped so no sequence reaches
// the terminal.
func TestSanitizeControlSequences_LeaksNonCSIOSCBodyButStripsESC(t *testing.T) {
	in := "before\x1bPmalicious\x1b\\after"
	want := "beforePmalicious\\after"
	if got := SanitizeControlSequences(in); got != want {
		t.Errorf("SanitizeControlSequences(%q) = %q, want %q", in, got, want)
	}
}

// Newline and tab are C0 controls but must survive, or rendered transcript
// formatting breaks.
func TestSanitizeControlSequences_PreservesNewlineAndTab(t *testing.T) {
	in := "line one\n\tindented"
	if got := SanitizeControlSequences(in); got != in {
		t.Errorf("SanitizeControlSequences(%q) = %q, want unchanged", in, got)
	}
}

// A raw invalid C1 byte decodes as U+FFFD, outside the 0x80-0x9f range
// check, so it survives instead of being stripped (#1019). That is
// documented harmless behavior, not a strip bug.
func TestSanitizeControlSequences_RawC1SurvivesAsReplacementChar(t *testing.T) {
	for _, raw := range []byte{0x9b, 0x9d} {
		in := string([]byte{'a', raw, 'b'})
		want := "a�b"
		if got := SanitizeControlSequences(in); got != want {
			t.Errorf("SanitizeControlSequences(%q) = %q, want %q", in, got, want)
		}
	}
}
