package runnertest

import "slices"

// ContainsArg reports whether args contains flag as an element.
func ContainsArg(args []string, flag string) bool {
	return slices.Contains(args, flag)
}

// ContainsTriple reports whether args contains the contiguous 3-element
// subsequence a0, a1, a2 (e.g. a flag and its two positional values).
func ContainsTriple(args []string, a0, a1, a2 string) bool {
	for i := 0; i+2 < len(args); i++ {
		if args[i] == a0 && args[i+1] == a1 && args[i+2] == a2 {
			return true
		}
	}
	return false
}
