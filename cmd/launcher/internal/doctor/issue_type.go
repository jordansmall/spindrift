package doctor

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"spindrift.dev/launcher/internal/forge"
)

// Names of the two issue-type Check rows, so callers match on the constants.
const (
	IssueTypeCheckName       = "issue-type"
	IssueTypeFieldsCheckName = "issue-type-required-fields"
)

// IssueTypeChecks builds the issue-type rows (issue #6352): Required that the
// configured type exists on the project, Advisory that no required create
// field is left unfilled. One create-metadata read feeds both. A tracker
// without the capability gets no rows.
func IssueTypeChecks(caps forge.Capabilities) []Check {
	checker := caps.IssueTypeChecker
	if checker == nil {
		return nil
	}
	var once sync.Once
	var unfilled []string
	var checkErr error
	fetch := func() ([]string, error) {
		once.Do(func() { unfilled, checkErr = checker.CheckIssueType() })
		return unfilled, checkErr
	}
	return []Check{
		{
			Name: IssueTypeCheckName,
			Tier: Required,
			Probe: func() (any, error) {
				_, err := fetch()
				switch {
				case err == nil:
					return nil, nil
				case errors.Is(err, forge.ErrUnknownIssueType):
					return nil, err
				default:
					// An unreadable createmeta (a permission gap) cannot show the
					// type is wrong, so it must not block Run.
					return nil, fmt.Errorf("issue type check failed: %w: %w", err, ErrDegraded)
				}
			},
			SuccessMsg: func(any) string { return "issue type is offered by the project" },
		},
		{
			Name: IssueTypeFieldsCheckName,
			Tier: Advisory,
			Probe: func() (any, error) {
				fields, err := fetch()
				if err != nil {
					// The issue-type row already reported this failure.
					return nil, nil
				}
				if len(fields) > 0 {
					return nil, fmt.Errorf("the create screen requires field(s) spindrift does not fill, so filing an issue will fail: %s", strings.Join(fields, ", "))
				}
				return nil, nil
			},
			SuccessMsg: func(any) string { return "no required create fields left unfilled" },
		},
	}
}
