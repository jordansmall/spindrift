package dispatch

import (
	"encoding/json"
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/signalsocket"
)

// signalResultFromBuffer projects buf into the same three field values the
// log scanners (LastCommentLineInLog, LastPRIntentInLog,
// AllIssueIntentLinesInLog) produce from equivalent marker lines, so
// outcomeResult can fill Result identically from either carrier (issue
// #3725). A nil buf (an attempt that never started a listener under
// BOX_SIGNAL_CARRIER=socket) degrades to every signal absent, matching a log
// with no marker lines at all.
func signalResultFromBuffer(buf *signalsocket.Buffer) (comment string, commentFound bool, prIntent string, prIntentFound bool, issueIntents []string) {
	if buf == nil {
		return "", false, "", false, nil
	}

	if c, ok := buf.Comment(); ok {
		comment, commentFound = c.Body, true
	}

	if p, ok := buf.PRIntent(); ok {
		// "\n\n" is the exact separator parsePRIntent (settle/pr_intent.go)
		// splits on to recover title/body; the log carrier's payload is this
		// same shape, and the two carriers must produce byte-identical
		// Result.PRIntent values for this ticket's contract to hold.
		prIntent, prIntentFound = p.Title+"\n\n"+p.Body, true
	}

	for _, i := range buf.IssueIntents() {
		// Marshalled JSON, not the struct itself, because Result.IssueIntents
		// is []string on both carriers: settle's parseIssueIntent decodes
		// each entry as JSON regardless of which carrier produced it, so the
		// socket carrier must hand it the same encoded payload a log marker
		// line would have carried.
		raw, err := json.Marshal(i)
		if err != nil {
			// signalwire.IssueIntent has no unmarshalable field (plain
			// strings and a []string), so this is unreachable outside a
			// future field addition. Loud on stderr rather than a panic:
			// every other scan failure in outcomeResult warns and carries
			// on, and a panic here would take down sibling Dispatches that
			// have nothing to do with this buffer.
			fmt.Fprintf(os.Stderr, "    ?? signal socket: marshal issue intent: %v\n", err)
			continue
		}
		issueIntents = append(issueIntents, string(raw))
	}

	return comment, commentFound, prIntent, prIntentFound, issueIntents
}
