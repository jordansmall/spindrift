package usage_test

import (
	"testing"

	"spindrift.dev/launcher/internal/usage"
)

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, "0s"},
		{999, "0s"},
		{1000, "1s"},
		{5000, "5s"},
		{59000, "59s"},
		{60000, "1m 0s"},
		{65000, "1m 5s"},
		{3600000, "1h 0m 0s"},
		{3665000, "1h 1m 5s"},
		{7384000, "2h 3m 4s"},
	}
	for _, tc := range cases {
		got := usage.FormatDuration(tc.ms)
		if got != tc.want {
			t.Errorf("FormatDuration(%d): got %q, want %q", tc.ms, got, tc.want)
		}
	}
}
