package unixsocket

import (
	"runtime"
	"strings"
	"testing"
)

// The made-up GOOS confirms 108 is the default branch, not a "linux" match
// (issue #3077).
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

// A path exactly at the sun_path cap does not fit (issue #3077): the kernel
// needs the last byte for its NUL terminator.
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
