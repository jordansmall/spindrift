package connectortracker

import (
	"testing"

	"spindrift.dev/launcher/internal/connectorprotocol/wiretest"
)

// TestWireShapesAgree holds core's tracker wire structs to the shapes the
// protocol description gives them, which the SDK's twins are rendered from.
func TestWireShapesAgree(t *testing.T) {
	for key, v := range map[string]any{
		"trackerTarget":     Target{},
		"route":             Selector{},
		"issue":             wireIssue{},
		"comment":           wireComment{},
		"createField":       wireCreateField{},
		"statusTarget":      StatusTarget{},
		"transitionRequest": transitionRequest{},
		"transitionResult":  transitionResult{},
		"commentRequest":    commentRequest{},
		"issueRequest":      issueRequest{},
		// The write methods' params, which the SDK's handlers decode.
		"tracker.transition.params":  transitionParams{},
		"tracker.editLabels.params":  editLabelsParams{},
		"tracker.createLabel.params": createLabelParams{},
		"tracker.comment.params":     commentParams{},
		"tracker.fileIssue.params":   fileIssueParams{},
		"tracker.closeIssue.params":  closeIssueParams{},
	} {
		wiretest.Agree(t, key, v)
	}
}
