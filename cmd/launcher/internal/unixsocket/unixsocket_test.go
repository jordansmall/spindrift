package unixsocket

import (
	"runtime"
	"strings"
	"testing"
)

// TestSunPathCap verifies sunPathCap returns the AF_UNIX sun_path byte cap
// for each platform (issue #3077): 104 on darwin, and 108 for anything else,
// checked against both "linux" and a made-up GOOS to confirm the 108 branch
// is a genuine default case rather than a "linux"-specific match.
func TestSunPathCap(t *testing.T) {
	cases := []struct {
		goos string
		want int
	}{
		{goos: "darwin", want: 104},
		{goos: "linux", want: 108},
		{goos: "made-up-os", want: 108},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			if got := sunPathCap(tc.goos); got != tc.want {
				t.Errorf("sunPathCap(%q) = %d, want %d", tc.goos, got, tc.want)
			}
		})
	}
}

// TestTooLong_Boundary verifies TooLong's off-by-one boundary: a path one
// byte under the running platform's sun_path cap fits, while a path exactly
// at the cap does not (issue #3077) -- the kernel needs the last byte for
// its own NUL terminator.
func TestTooLong_Boundary(t *testing.T) {
	sunPathLimit := sunPathCap(runtime.GOOS)
	fits := strings.Repeat("a", sunPathLimit-1)
	if TooLong(fits) {
		t.Errorf("TooLong(path of length %d) = true, want false (cap is %d)", len(fits), sunPathLimit)
	}
	tooLong := strings.Repeat("a", sunPathLimit)
	if !TooLong(tooLong) {
		t.Errorf("TooLong(path of length %d) = false, want true (cap is %d)", len(tooLong), sunPathLimit)
	}
}
