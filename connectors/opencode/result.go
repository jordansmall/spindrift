package main

import "encoding/json"

// ResultEvent encodes text as one newline-terminated NDJSON text event, the
// inverse of the host's last-message reader: a synthetic final result the
// orchestrator appends to the stream log (issue #4406).
func ResultEvent(text string) ([]byte, error) {
	type part struct {
		Text string `json:"text"`
	}
	b, err := json.Marshal(struct {
		Type string `json:"type"`
		Part part   `json:"part"`
	}{"text", part{text}})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
