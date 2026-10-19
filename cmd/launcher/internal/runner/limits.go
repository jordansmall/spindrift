package runner

import (
	"fmt"
	"math"
	"strconv"
)

// MemoryLimitToBytes converts a podman/docker-style unit-suffixed memory limit
// ("5g", "512m", "1024k"; bare digits are already bytes) to a raw byte count.
// cgroup v2's memory.max accepts only a plain integer or the literal "max",
// unlike podman's --memory -- so it is the output side that must be bytes;
// "max" is not an input shape this parses. The doctor-side podman-machine
// sizing check needs the same conversion, so this is the one parse both
// share. It errors on a non-positive value or one whose byte count overflows
// int64, so "0" is an error, not "no limit".
func MemoryLimitToBytes(limit string) (int64, error) {
	if limit == "" {
		return 0, fmt.Errorf("empty memory limit")
	}
	mult := int64(1)
	numPart := limit
	switch limit[len(limit)-1] {
	case 'g', 'G':
		mult = 1024 * 1024 * 1024
		numPart = limit[:len(limit)-1]
	case 'm', 'M':
		mult = 1024 * 1024
		numPart = limit[:len(limit)-1]
	case 'k', 'K':
		mult = 1024
		numPart = limit[:len(limit)-1]
	}
	n, err := strconv.ParseInt(numPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid memory limit %q: %w", limit, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("invalid memory limit %q: must be positive (set MEMORY_LIMIT to empty to disable the limit)", limit)
	}
	if n > math.MaxInt64/mult {
		return 0, fmt.Errorf("invalid memory limit %q: overflows int64 bytes", limit)
	}
	return n * mult, nil
}
