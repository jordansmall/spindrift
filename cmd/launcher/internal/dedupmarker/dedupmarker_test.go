package dedupmarker

import (
	"slices"
	"testing"
)

func TestBuildParseRoundTrip(t *testing.T) {
	m := Build([]string{"a:b", "c"})
	if m != "<!-- spindrift-dedup: a:b, c -->" {
		t.Fatalf("Build = %q", m)
	}
	if got, want := Parse("prose\n\n"+m), []string{"a:b", "c"}; !slices.Equal(got, want) {
		t.Errorf("Parse = %v, want %v", got, want)
	}
}

func TestBuildNoTermsIsEmpty(t *testing.T) {
	if got := Build(nil); got != "" {
		t.Errorf("Build(nil) = %q, want empty", got)
	}
}

func TestBareMarkerParsesToNoTerms(t *testing.T) {
	if got := Parse("x\n\n" + Bare); len(got) != 0 {
		t.Errorf("Parse(Bare) = %v, want none", got)
	}
}

func TestParseNoMarker(t *testing.T) {
	if got := Parse("plain\nbody"); got != nil {
		t.Errorf("Parse = %v, want nil", got)
	}
}

func TestParseLastMarkerWins(t *testing.T) {
	body := "```\n" + Build([]string{"quoted"}) + "\n```\n\n" + Build([]string{"real"})
	if got, want := Parse(body), []string{"real"}; !slices.Equal(got, want) {
		t.Errorf("Parse = %v, want %v", got, want)
	}
}

func TestSplit(t *testing.T) {
	a, b := Build([]string{"a"}), Build([]string{"b"})
	cases := []struct {
		name, body, wantRest, wantMarker string
	}{
		{"no marker", "plain\nbody\n", "plain\nbody\n", ""},
		{"empty", "", "", ""},
		{"marker last", "text\n\n" + a, "text", a},
		{"marker last trailing newline", "text\n\n" + a + "\n", "text", a},
		{"marker only", a, "", a},
		{"multiple markers, last removed", a + "\n\ntext\n\n" + b, a + "\n\ntext", b},
		{"marker not last line", "head\n" + a + "\ntail", "head\ntail", a},
		{"marker with CRLF kept verbatim", "text\r\n\r\n" + a + "\r\n", "text", a + "\r"},
		{"indented marker", "text\n\n  " + a, "text", "  " + a},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rest, marker := Split(tc.body)
			if rest != tc.wantRest || marker != tc.wantMarker {
				t.Errorf("Split = (%q, %q), want (%q, %q)", rest, marker, tc.wantRest, tc.wantMarker)
			}
		})
	}
}

func TestAppend(t *testing.T) {
	m := Build([]string{"a"})
	if got := Append("text", m); got != "text\n\n"+m {
		t.Errorf("Append = %q", got)
	}
	if got := Append("", m); got != m {
		t.Errorf("Append(empty body) = %q", got)
	}
	if got := Append("text", ""); got != "text" {
		t.Errorf("Append(empty marker) = %q", got)
	}
}

func TestSplitAppendRoundTrip(t *testing.T) {
	a, b := Build([]string{"a", "b"}), Build([]string{"c"})
	for _, body := range []string{
		"text\n\n" + a,
		a + "\n\ntext\n\n" + b,
		"head\n" + b + "\ntail",
		"no marker",
		"",
		"text\n\n" + Bare,
	} {
		rest, marker := Split(body)
		if got, want := Parse(Append(rest, marker)), Parse(body); !slices.Equal(got, want) {
			t.Errorf("Parse(Append(Split(%q))) = %v, want %v", body, got, want)
		}
	}
}
