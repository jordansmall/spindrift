package runner

import (
	"math"
	"testing"
)

// memory.max's cgroup v2 kernel interface needs a raw byte count, unlike
// podman's own --memory flag, which accepts the suffixed string unconverted.
func TestMemoryLimitToBytes(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{in: "5g", want: 5 * 1024 * 1024 * 1024},
		{in: "5G", want: 5 * 1024 * 1024 * 1024},
		{in: "512m", want: 512 * 1024 * 1024},
		{in: "512M", want: 512 * 1024 * 1024},
		{in: "1024k", want: 1024 * 1024},
		{in: "1024K", want: 1024 * 1024},
		{in: "2048", want: 2048},
		{in: "", wantErr: true},
		{in: "not-a-number", wantErr: true},
		{in: "5x", wantErr: true},
		{in: "1", want: 1},
		{in: "+5g", want: 5 * 1024 * 1024 * 1024},
		{in: "0", wantErr: true},
		{in: "0g", wantErr: true},
		{in: "-1", wantErr: true},
		{in: "-1g", wantErr: true},
		{in: "9000000000g", wantErr: true},
		{in: "8589934591g", want: 8589934591 * 1024 * 1024 * 1024},
		{in: "8589934592g", wantErr: true},
		{in: "9223372036854775807", want: math.MaxInt64},
		{in: "9223372036854775808", wantErr: true},
	}
	for _, c := range cases {
		got, err := MemoryLimitToBytes(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("MemoryLimitToBytes(%q): want error, got %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("MemoryLimitToBytes(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("MemoryLimitToBytes(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
