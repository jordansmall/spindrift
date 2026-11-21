package driverkit

import "strings"

// resultTextMaxLen keeps one noisy tool call (a huge file read, a long
// command's stdout) from dominating the transcript.
const resultTextMaxLen = 200

// RenderTranscript returns the Pass log at logPath as "[role]"-prefixed lines
// in log order, ending in "\n" when non-empty: a tool call renders
// "[role] Name(target)" and its result "[role]   -> summary", tagged
// "[subagent] " when it carries a subagent's report. Only a text event's first
// line is prefixed and its embedded newlines stay verbatim, because the
// orchestrator's verdict scan counts only prefixed lines and slices review
// findings from the bare lines after; text is trimmed so a leading newline
// cannot push VERDICT: off the prefixed line. Tool-controlled text is
// flattened to one line so it cannot start a line of its own. An event with
// no role takes topLevelRole (default ImplementorRole). A missing log is "".
func RenderTranscript(logPath string, newDecoder func() Decoder, topLevelRole string) (string, error) {
	l, err := DecodeLog(logPath, newDecoder)
	if err != nil {
		return "", err
	}
	return l.renderTranscript(topLevelRole), nil
}

func (l *Log) renderTranscript(topLevelRole string) string {
	if topLevelRole == "" {
		topLevelRole = ImplementorRole
	}
	var lines []string
	l.Each(func(ev Event) {
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
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
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
