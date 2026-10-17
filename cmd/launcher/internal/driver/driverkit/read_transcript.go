package driverkit

import "strings"

// RenderTranscript returns each non-empty KindText event of the Pass log at
// logPath, joined by "\n" in log order. The first physical line of each event
// is prefixed "[role] ", because the orchestrator's verdict scan counts only
// "[role]"-prefixed lines and slices the review findings from the lines that
// follow; embedded newlines stay verbatim so continuation lines remain bare.
// The role is the event's own, or topLevelRole (defaulting to
// ImplementorRole) for an event with none. Text is trimmed so a leading
// newline cannot push a VERDICT: line off the prefixed first line. Returns
// ("", nil) when logPath does not exist, as the claude Driver does.
func RenderTranscript(logPath string, newDecoder func() Decoder, topLevelRole string) (string, error) {
	if topLevelRole == "" {
		topLevelRole = ImplementorRole
	}
	var texts []string
	err := scanEvents(logPath, newDecoder, func(ev Event) {
		if ev.Kind != KindText {
			return
		}
		text := strings.TrimSpace(ev.Text)
		if text == "" {
			return
		}
		role := ev.Role
		if role == "" {
			role = topLevelRole
		}
		texts = append(texts, "["+role+"] "+text)
	})
	if err != nil {
		return "", err
	}
	return strings.Join(texts, "\n"), nil
}
