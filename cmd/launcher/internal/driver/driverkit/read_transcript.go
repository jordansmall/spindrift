package driverkit

import "strings"

// resultTextMaxLen keeps one noisy tool call (a huge file read, a long
// command's stdout) from dominating the transcript.
const resultTextMaxLen = 200

// RenderTranscript returns the Pass log at logPath as "[role]"-prefixed lines,
// joined by "\n" in log order, in the claude Driver's transcript form: a tool
// call renders "[role] Name(target)" and its result "[role]   -> summary",
// tagged "[subagent] " when it carries a subagent's report. The first physical
// line of a text event is prefixed because the
// orchestrator's verdict scan counts only "[role]"-prefixed lines and slices the
// review findings from the lines that follow; embedded newlines stay verbatim
// so continuation lines remain bare. Tool targets, results and subagent tags
// are flattened to one line instead, so tool-controlled text cannot start a
// line of its own. The role is the event's own, or
// topLevelRole (defaulting to ImplementorRole) for an event with none. Text is
// trimmed so a leading newline cannot push a VERDICT: line off the prefixed
// first line. Returns ("", nil) when logPath does not exist, as the claude
// Driver does.
func RenderTranscript(logPath string, newDecoder func() Decoder, topLevelRole string) (string, error) {
	if topLevelRole == "" {
		topLevelRole = ImplementorRole
	}
	var lines []string
	err := scanEvents(logPath, newDecoder, func(ev Event) {
		role := ev.Role
		if role == "" {
			role = topLevelRole
		}
		switch ev.Kind {
		case KindText:
			if text := strings.TrimSpace(ev.Text); text != "" {
				lines = append(lines, "["+role+"] "+text)
			}
		case KindToolCall:
			lines = append(lines, "["+role+"] "+ev.ToolName+"("+flattenToolText(ev.ToolTarget)+")")
		case KindToolResult:
			summary := SummarizeToolOutput(ev.ToolOutput, ev.IsError)
			if ev.Subagent != "" {
				summary = "[" + flattenToolText(ev.Subagent) + "] " + summary
			}
			lines = append(lines, "["+role+"]   -> "+summary)
		}
	})
	if err != nil {
		return "", err
	}
	return strings.Join(lines, "\n"), nil
}

var lineBreaks = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")

// flattenToolText replaces every line break in tool-controlled text with a
// space, so it cannot begin a physical transcript line of its own.
func flattenToolText(s string) string {
	return lineBreaks.Replace(s)
}

// SummarizeToolOutput flattens a tool result's output to one line for a
// transcript's "->" line. Every Driver's transcript calls it, so their result
// lines cannot drift apart. A leading "[" is escaped as "\[" so tool output
// cannot pass for a "[role]" tag after "-> " (an error result starts "error: "
// already); the genuine subagent tag is prepended by the caller afterwards.
func SummarizeToolOutput(output string, isError bool) string {
	text := strings.TrimSpace(flattenToolText(output))
	if !isError && strings.HasPrefix(text, "[") {
		text = `\` + text
	}
	if len(text) > resultTextMaxLen {
		if runes := []rune(text); len(runes) > resultTextMaxLen {
			text = string(runes[:resultTextMaxLen-3]) + "..."
		}
	}
	if text == "" {
		text = "(empty result)"
	}
	if isError {
		return "error: " + text
	}
	return text
}
