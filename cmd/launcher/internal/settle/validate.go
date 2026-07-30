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

// ValidateMergeMethod checks method against the three documented
// MERGE_METHOD values. Unlike ValidateMergeMode, the value it guards is
// not carried on settle.Config: main.go's validate calls this on the raw
// knob, which is then threaded into the github adapter via
// github.WithMergeMethod rather than consumed by settle.New.
func ValidateMergeMethod(method string) error {
	switch method {
	case "merge", "squash", "rebase":
		return nil
	default:
		return fmt.Errorf("MERGE_METHOD=%q is not valid; must be merge, squash, or rebase", method)
	}
}

// ValidateSyncMethod checks method against the two documented SYNC_METHOD
// values. Like ValidateMergeMethod, the value it guards is not carried on
// settle.Config: main.go's validate calls this on the raw knob, which is
// then threaded into the github adapter via github.WithSyncMethod rather
// than consumed by settle.New.
func ValidateSyncMethod(method string) error {
	switch method {
	case "rebase", "merge":
		return nil
	default:
		return fmt.Errorf("SYNC_METHOD=%q is not valid; must be rebase or merge", method)
	}
}
