package runner

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Fixed caps for the Connector sandbox on every runner, deliberately not
// Config's per-Box knobs: it holds only connector-host. The memory cap leaves
// room for several Connector modules (each capped near 64 MiB by connectorhost)
// plus the host's Go runtime; the pids cap covers that runtime's threads.
const (
	SandboxPidsLimit   = "128"
	SandboxMemoryLimit = "512m"
)

// MemoryLimitToBytes converts a podman/docker-style unit-suffixed memory
// limit ("5g", "512m", "1024k", "512b", "8GiB", "1.5t"; bare digits are
// already bytes) to a raw byte count, since cgroup v2's memory.max takes only
// an integer or "max" ("max" is not an accepted input). The grammar follows
// go-units' RAMInBytes, always 1024-binary, with the narrowings below. The
// doctor-side podman-machine sizing check shares this parse. A non-positive
// value or int64 overflow is an error, so "0" is not "no limit".
func MemoryLimitToBytes(limit string) (int64, error) {
	if limit == "" {
		return 0, fmt.Errorf("empty memory limit")
	}
	scale := int64(1)
	numPart := limit
	if c := numPart[len(numPart)-1]; c == 'b' || c == 'B' {
		numPart = numPart[:len(numPart)-1]
	}
	// "i" decorates a unit letter ("ki", "GiB"), so only strip it when a
	// unit letter precedes it; "8ib" or a lone "i" is left for the number
	// parse to reject.
	if n := len(numPart); n >= 2 && (numPart[n-1] == 'i' || numPart[n-1] == 'I') && isMemoryUnitLetter(numPart[n-2]) {
		numPart = numPart[:n-1]
	}
	if n := len(numPart); n > 0 {
		if m, ok := memoryUnitMultiplier(numPart[n-1]); ok {
			scale = m
			numPart = numPart[:n-1]
		}
	}
	// go-units' RAMInBytes regex allows a single space between the number
	// and its suffix ("1.5 g", "512 b"); the suffix strip above leaves it.
	if strings.HasSuffix(numPart, " ") {
		numPart = numPart[:len(numPart)-1]
	}
	if !validMemoryNumber(numPart) {
		return 0, fmt.Errorf("invalid memory limit %q: invalid syntax", limit)
	}
	if strings.IndexByte(numPart, '.') >= 0 {
		p, err := strconv.ParseFloat(numPart, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid memory limit %q: %w", limit, err)
		}
		product := p * float64(scale)
		// float64(MaxInt64) rounds up to 2^63, so reaching it already means
		// the true value does not fit in int64.
		if product >= float64(math.MaxInt64) {
			return 0, memoryLimitOverflowErr(limit)
		}
		// Check the truncated byte count, not the float: a decimal whose
		// product is positive but below one byte ("0.0000001k") truncates to
		// a zero limit, which must stay an error like "0".
		if b := int64(product); b <= 0 {
			return 0, memoryLimitPositiveErr(limit)
		}
		return int64(product), nil
	}
	n, err := strconv.ParseInt(numPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid memory limit %q: %w", limit, err)
	}
	if n <= 0 {
		return 0, memoryLimitPositiveErr(limit)
	}
	if n > math.MaxInt64/scale {
		return 0, memoryLimitOverflowErr(limit)
	}
	return n * scale, nil
}

func memoryLimitPositiveErr(limit string) error {
	return fmt.Errorf("invalid memory limit %q: must be positive (set MEMORY_LIMIT to empty to disable the limit)", limit)
}

func memoryLimitOverflowErr(limit string) error {
	return fmt.Errorf("invalid memory limit %q: overflows int64 bytes", limit)
}

// memoryUnitMultiplier maps one of the k/m/g/t/p unit letters, in either
// case, to its 1024-binary byte multiplier.
func memoryUnitMultiplier(c byte) (int64, bool) {
	switch c {
	case 'k', 'K':
		return 1024, true
	case 'm', 'M':
		return 1024 * 1024, true
	case 'g', 'G':
		return 1024 * 1024 * 1024, true
	case 't', 'T':
		return 1024 * 1024 * 1024 * 1024, true
	case 'p', 'P':
		return 1024 * 1024 * 1024 * 1024 * 1024, true
	}
	return 0, false
}

// isMemoryUnitLetter reports whether c is one of the k/m/g/t/p unit letters
// in either case.
func isMemoryUnitLetter(c byte) bool {
	_, ok := memoryUnitMultiplier(c)
	return ok
}

// validMemoryNumber reports whether s is a plain number — an optional
// leading "+" followed by digits with at most one decimal point flanked by
// digits — keeping out strconv's scientific-notation, sign, Inf, and NaN
// forms.
func validMemoryNumber(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '+' {
		s = s[1:]
	}
	dot := -1
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] >= '0' && s[i] <= '9':
		case s[i] == '.' && dot == -1:
			dot = i
		default:
			return false
		}
	}
	return len(s) > 0 && dot != 0 && dot != len(s)-1
}
