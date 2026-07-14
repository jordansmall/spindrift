package console

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// Run drives the console's read-render loop: load the backlog, render it,
// then repeatedly read one command per line from in and re-render until the
// operator quits or in runs out. It is the only place that touches a real
// terminal in production; tests drive it with a scripted io.Reader instead.
func Run(tracker forge.IssueTracker, pwd string, in io.Reader, out io.Writer) error {
	m := NewModel()
	m = Update(m, DogfoodNotice(pwd))
	m = Update(m, Refresh(tracker))
	fmt.Fprint(out, View(m))

	scanner := bufio.NewScanner(in)
	for !m.Quitting && scanner.Scan() {
		m = applyCommand(m, tracker, scanner.Text())
		if !m.Quitting {
			fmt.Fprint(out, View(m))
		}
	}
	return scanner.Err()
}

// applyCommand parses one line of operator input into a Msg and applies it.
// Recognized commands: "q"/"quit" to exit, "r"/"refresh" to re-query the
// tracker, "f" (bare) to clear the label filter, "f <text>" to set it.
func applyCommand(m Model, tracker forge.IssueTracker, line string) Model {
	cmd, arg, _ := strings.Cut(strings.TrimSpace(line), " ")
	switch cmd {
	case "q", "quit":
		return Update(m, QuitMsg{})
	case "r", "refresh":
		return Update(m, Refresh(tracker))
	case "f", "filter":
		return Update(m, FilterChangedMsg{Filter: arg})
	default:
		return m
	}
}
