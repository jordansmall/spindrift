package outcome_test

import (
	"testing"

	"spindrift.dev/launcher/internal/outcome"
)

const canonicalLine = "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=ok"

func TestStripResultText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"bold", "**" + canonicalLine + "**", canonicalLine},
		{"backtick", "`" + canonicalLine + "`", canonicalLine},
		{"surrounding space", "  \t" + canonicalLine + " \t", canonicalLine},
		{"bold with inner space", "  **" + canonicalLine + "**  ", canonicalLine},
		{"per line, trailing newline kept", "**a**\n`b`\n", "a\nb\n"},
		{"no trailing newline kept", "**a**\nb", "a\nb"},
		{"interior markup untouched", "a **b** c", "a **b** c"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := outcome.StripResultText(tc.in); got != tc.want {
				t.Errorf("StripResultText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExtractOutcomeLine(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"canonical", canonicalLine + "\n", canonicalLine},
		{"colon normalized", "SPINDRIFT_OUTCOME: issue=7 landing=agent/issue-7 status=ready note=ok\n", canonicalLine},
		{"colon with extra space normalized", "SPINDRIFT_OUTCOME:   issue=7 landing=agent/issue-7 status=ready note=ok", canonicalLine},
		{"missing landing", "SPINDRIFT_OUTCOME issue=7 status=ready\n", ""},
		{"missing status", "SPINDRIFT_OUTCOME issue=7 landing=x\n", ""},
		{"near miss not returned", "SPINDRIFT_OUTCOME: Complete -- nothing more to report\n", ""},
		{"token not leading", "see " + canonicalLine + "\n", ""},
		{"token glued to suffix", "SPINDRIFT_OUTCOMEX landing=a status=b\n", ""},
		{"landing glued to prefix", "SPINDRIFT_OUTCOME xlanding=a status=b\n", ""},
		{"tab separator is not a field boundary", "SPINDRIFT_OUTCOME\tlanding=a\tstatus=b\n", ""},
		{"last wins", "SPINDRIFT_OUTCOME issue=1 landing=a status=ready\nprose\nSPINDRIFT_OUTCOME issue=2 landing=b status=blocked\n", "SPINDRIFT_OUTCOME issue=2 landing=b status=blocked"},
		{"last matching wins over later near miss", canonicalLine + "\nSPINDRIFT_OUTCOME: oops\n", canonicalLine},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := outcome.ExtractOutcomeLine(tc.in); got != tc.want {
				t.Errorf("ExtractOutcomeLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExtractOutcomeLine_AfterStrip(t *testing.T) {
	in := "done\n**SPINDRIFT_OUTCOME: issue=7 landing=agent/issue-7 status=ready note=ok**\n"
	if got := outcome.ExtractOutcomeLine(outcome.StripResultText(in)); got != canonicalLine {
		t.Errorf("got %q, want %q", got, canonicalLine)
	}
}
