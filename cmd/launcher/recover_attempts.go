package main

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/recoverrecord"
)

// maxCauseBytes bounds a stored cause: it lands verbatim in a public issue
// comment and a one-line stdout note.
const maxCauseBytes = 500

// normalizeCause collapses s to one line and caps it at maxCauseBytes without
// cutting a rune.
func normalizeCause(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxCauseBytes {
		return s
	}
	cut := maxCauseBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// giveUpRecover posts the give-up comment and sets r.GaveUp. A failed post is
// logged and leaves GaveUp false, so a later pass retries. It does not save; the
// caller does.
func giveUpRecover(r *recoverrecord.Record, it forge.IssueTracker, num string, stderr io.Writer) {
	cause := ": the outbox bundle relay or draft PR creation kept failing"
	if r.LastError != "" {
		cause = "; the last one failed with: " + r.LastError
	}
	body := fmt.Sprintf("Auto-recover gave up on this issue after %d attempts%s. It will not retry this bundle again; fix the cause, then run `spindrift recover %s`.", r.Count, cause, num)
	if err := it.Comment(num, body); err != nil {
		fmt.Fprintf(stderr, "    ?? #%s: comment: %v\n", num, err)
		return
	}
	r.GaveUp = true
}
