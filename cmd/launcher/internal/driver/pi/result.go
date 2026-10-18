package pi

import "encoding/json"

// ResultEvent encodes text as one newline-terminated assistant message_end with
// a single text block, the inverse of the Driver's ResultText reader
// (driverkit.LastMessageText over Decode): a synthetic final result the
// orchestrator appends to the stream log (issue #4406). It carries no
// timestamp, so the decoder gives it no message id and it stays the last
// message, and no usage, so it adds no turn.
func ResultEvent(text string) ([]byte, error) {
	type block struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type message struct {
		Role       string  `json:"role"`
		Content    []block `json:"content"`
		StopReason string  `json:"stopReason"`
	}
	b, err := json.Marshal(struct {
		Type    string  `json:"type"`
		Message message `json:"message"`
	}{"message_end", message{"assistant", []block{{"text", text}}, "stop"}})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
