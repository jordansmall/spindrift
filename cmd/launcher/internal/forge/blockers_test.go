package forge_test

import (
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

func TestIsBlockedByHeader(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"## Blocked by", true},
		{"  ## Blocked by:", true},
		{"## Touches", false},
		{"not a header", false},
	}
	for _, c := range cases {
		if got := forge.IsBlockedByHeader(c.line); got != c.want {
			t.Errorf("IsBlockedByHeader(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestIsAnyHeading(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"## Touches", true},
		{"# Title", true},
		{"#", true},
		{"###", true},
		{"######", true},
		{"#\tTab", true},
		{"#6 is related", false},
		{"#hashtag", false},
		{"##Context", false},
		{"####### seven", false},
		{"  ## Indented", false},
		{"- item", false},
		{"plain text", false},
	}
	for _, c := range cases {
		if got := forge.IsAnyHeading(c.line); got != c.want {
			t.Errorf("IsAnyHeading(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestIsBulletItem(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"- item", true},
		{"  * item", true},
		{"## Touches", false},
		{"plain text", false},
	}
	for _, c := range cases {
		if got := forge.IsBulletItem(c.line); got != c.want {
			t.Errorf("IsBulletItem(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestIsFenceDelimiter(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"```", true},
		{"```go", true},
		{"~~~", true},
		{"~~~~", true},
		{"``", false},
		{"~~", false},
		{"plain text", false},
		{"- item", false},
	}
	for _, c := range cases {
		if got := forge.IsFenceDelimiter(c.line); got != c.want {
			t.Errorf("IsFenceDelimiter(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestStripInlineCode(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"before `code` after", "before  after"},
		{"`a` and `b`", " and "},
		{"no backticks here", "no backticks here"},
		{"unterminated `span", "unterminated `span"},
	}
	for _, c := range cases {
		if got := forge.StripInlineCode(c.line); got != c.want {
			t.Errorf("StripInlineCode(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

func TestExtractBulletContent(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"- foo/bar", "foo/bar"},
		{"  * baz  ", "baz"},
		{"\t- foo", "foo"},
		{"-\tbar", "bar"},
		{"\t*\tqux", "qux"},
	}
	for _, c := range cases {
		if got := forge.ExtractBulletContent(c.line); got != c.want {
			t.Errorf("ExtractBulletContent(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

func TestUnfencedLines(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"no fences", "a\nb", []string{"a", "b"}},
		{"backtick fence", "a\n```\nhidden\n```\nb", []string{"a", "b"}},
		{"tilde fence", "a\n~~~\nhidden\n~~~\nb", []string{"a", "b"}},
		{"info string", "a\n```go\nhidden\n```\nb", []string{"a", "b"}},
		{"CRLF", "a\r\n```\r\nhidden\r\n```\r\nb\r\n", []string{"a", "b", ""}},
		{"lone trailing CR", "a\r\r\nb\r", []string{"a", "b"}},
		{"unclosed fence drops the rest", "a\n```\nhidden\nmore", []string{"a"}},
		{"tilde closes backtick fence", "a\n```\nhidden\n~~~\nb", []string{"a", "b"}},
		{"empty body", "", []string{""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := slices.Collect(forge.UnfencedLines(c.body)); !slices.Equal(got, c.want) {
				t.Errorf("UnfencedLines(%q) = %q, want %q", c.body, got, c.want)
			}
		})
	}
	t.Run("early break", func(t *testing.T) {
		n := 0
		for range forge.UnfencedLines("a\nb\nc") {
			n++
			break
		}
		if n != 1 {
			t.Errorf("iterated %d times after break, want 1", n)
		}
	})
}
