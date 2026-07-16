package forge_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

func TestDepSource_String(t *testing.T) {
	cases := []struct {
		name   string
		source forge.DepSource
		want   string
	}{
		{"native", forge.DepSourceNative, "native"},
		{"body", forge.DepSourceBody, "body"},
		{"unknown", forge.DepSourceUnknown, "unknown"},
		{"out of range", forge.DepSource(99), "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.source.String(); got != c.want {
				t.Errorf("String() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRef(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		source forge.DepSource
		want   string
	}{
		{"native", "42", forge.DepSourceNative, "#42 (native)"},
		{"body", "43", forge.DepSourceBody, "#43 (body)"},
		{"unknown", "44", forge.DepSourceUnknown, "#44 (unknown)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := forge.Ref(c.id, c.source); got != c.want {
				t.Errorf("Ref(%q, %v) = %q, want %q", c.id, c.source, got, c.want)
			}
		})
	}
}
