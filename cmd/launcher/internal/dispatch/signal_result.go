package dispatch

import (
	"encoding/json"
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/signalsocket"
)

// signalResultFromBuffer projects buf into the same field values the log
// scanners produce from equivalent marker lines, so outcomeResult fills
// Result identically from either carrier (issue #3725). A nil buf degrades to
// every signal absent.
func signalResultFromBuffer(buf *signalsocket.Buffer) (comment string, commentFound bool, prIntent string, prIntentFound bool, issueIntents []string) {
	if buf == nil {
		return "", false, "", false, nil
	}

	if c, ok := buf.Comment(); ok {
		comment, commentFound = c.Body, true
	}

	if p, ok := buf.PRIntent(); ok {
		// "\n\n" is the separator settle's parsePRIntent splits on; the log
		// carrier's payload has the same shape.
		prIntent, prIntentFound = p.Title+"\n\n"+p.Body, true
	}

	for _, i := range buf.IssueIntents() {
		// settle decodes each entry as JSON whichever carrier produced it.
		raw, err := json.Marshal(i)
		if err != nil {
			// Unreachable today; warn rather than panic, which would take
			// down sibling Dispatches.
			fmt.Fprintf(os.Stderr, "    ?? signal socket: marshal issue intent: %v\n", err)
			continue
		}
		issueIntents = append(issueIntents, string(raw))
	}

	return comment, commentFound, prIntent, prIntentFound, issueIntents
}
