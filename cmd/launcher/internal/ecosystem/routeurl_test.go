package ecosystem

import "testing"

func TestRouteLocalURL(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		want   string
	}{
		{
			name:   "prefixed route",
			prefix: "r0",
			want:   "http://127.0.0.1:27182/r0",
		},
		{
			name:   "empty prefix",
			prefix: "",
			want:   "http://127.0.0.1:27182/",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RouteLocalURL(tc.prefix, 27182)
			if got != tc.want {
				t.Errorf("RouteLocalURL(%q, 27182) = %q, want %q", tc.prefix, got, tc.want)
			}
		})
	}
}
