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
