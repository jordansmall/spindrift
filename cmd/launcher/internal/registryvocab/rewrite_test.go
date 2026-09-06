package registryvocab

import "testing"

func TestJoinBase(t *testing.T) {
	tests := []struct {
		name string
		base string
		rel  string
		want string
	}{
		{
			name: "root sentinel contributes no segment",
			base: "/",
			rel:  "/config.json",
			want: "/config.json",
		},
		{
			name: "non-root single-segment base",
			base: "/index",
			rel:  "/config.json",
			want: "/index/config.json",
		},
		{
			name: "base with its own multi-segment path",
			base: "/index/foo/bar",
			rel:  "/config.json",
			want: "/index/foo/bar/config.json",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := JoinBase(tt.base, tt.rel); got != tt.want {
				t.Errorf("JoinBase(%q, %q) = %q, want %q", tt.base, tt.rel, got, tt.want)
			}
		})
	}
}
