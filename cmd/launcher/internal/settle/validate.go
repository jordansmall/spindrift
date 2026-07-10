package settle

import "fmt"

// ValidateMergeMode checks mode against the three documented MERGE_MODE
// values, guarding the same Config.MergeMode field New consumes.
func ValidateMergeMode(mode string) error {
	switch mode {
	case "immediate", "auto", "manual":
		return nil
	default:
		return fmt.Errorf("MERGE_MODE=%q is not valid; must be immediate, auto, or manual", mode)
	}
}
