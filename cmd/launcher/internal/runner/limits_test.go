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
		// Trailing bare "b" (bytes) and i/b decorations after a unit letter.
		{in: "512b", want: 512},
		{in: "8gb", want: 8 * 1024 * 1024 * 1024},
		{in: "8GB", want: 8 * 1024 * 1024 * 1024},
		{in: "8GiB", want: 8 * 1024 * 1024 * 1024},
		{in: "512MiB", want: 512 * 1024 * 1024},
		{in: "5ki", want: 5 * 1024},
		{in: "5kb", want: 5 * 1024},
		{in: "5kib", want: 5 * 1024},
		// t/p units and decimal values, still on the 1024-binary scale.
		{in: "1t", want: 1024 * 1024 * 1024 * 1024},
		{in: "1p", want: 1024 * 1024 * 1024 * 1024 * 1024},
		{in: "1.5g", want: 1610612736},
		{in: "2.5m", want: 2621440},
		{in: "1.5t", want: 1649267441664},
		{in: "0.0g", wantErr: true},
		{in: "0.0000001k", wantErr: true},
		{in: "0.5b", wantErr: true},
		{in: "0.5k", want: 512},
		{in: "8192p", wantErr: true},
		// A single space between number and suffix, as RAMInBytes allows.
		{in: "1.5 g", want: 1610612736},
		{in: "512 b", want: 512},
		{in: "1  g", wantErr: true},
		// Shapes RAMInBytes-style parsers must still reject.
		{in: "b", wantErr: true},
		{in: "ib", wantErr: true},
		{in: "8ib", wantErr: true},
		{in: "1e5", wantErr: true},
		{in: "1..2", wantErr: true},
		{in: ".5", wantErr: true},
		{in: "5.", wantErr: true},
		{in: "abc1", wantErr: true},
		// float64(MaxInt64) rounds 2^63-1 up to 2^63, so the decimal form of
		// MaxInt64 trips the decimal overflow check and errors.
		{in: "9223372036854775807.0", wantErr: true},
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
