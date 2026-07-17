package forge_test

import (
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
