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
// launch is nil for a launch-less session (Pick still promotes and queues,
// but nothing runs); production wires a real Launcher.
func Run(tracker forge.IssueTracker, pwd string, in io.Reader, out io.Writer, launch *Launcher) error {
	m := NewModel()
	m = Update(m, DogfoodNotice(pwd))
	m = Update(m, Refresh(tracker))
	fmt.Fprint(out, View(m))

	scanner := bufio.NewScanner(in)
	for !m.Quitting && scanner.Scan() {
		m = applyCommand(m, tracker, pwd, launch, scanner.Text())
		if !m.Quitting {
			fmt.Fprint(out, View(m))
		}
	}
	return scanner.Err()
}

// applyCommand parses one line of operator input into a Msg and applies it.
// Recognized commands: "q"/"quit" to exit, "r"/"refresh" to re-query the
// tracker, "f" (bare) to clear the label filter, "f <text>" to set it,
// "p <num>" to pick an issue, "u <num>" to unpick a queued one. A successful
// pick also lands on launch.Queue and kicks off a background launch attempt,
// when launch is non-nil.
func applyCommand(m Model, tracker forge.IssueTracker, pwd string, launch *Launcher, line string) Model {
	cmd, arg, _ := strings.Cut(strings.TrimSpace(line), " ")
	switch cmd {
	case "q", "quit":
		return Update(m, QuitMsg{})
	case "r", "refresh":
		return Update(m, Refresh(tracker))
	case "f", "filter":
		return Update(m, FilterChangedMsg{Filter: arg})
	case "p", "pick":
		msg := PickIssue(tracker, arg, titleOf(m, arg), KindWork)
		if queued, ok := msg.(PickQueuedMsg); ok && launch != nil {
			launch.Queue.Add(Pick{Number: queued.Number, Title: queued.Title, Kind: queued.Kind, State: PickQueued})
			launch.tryLaunch(tracker, pwd)
		}
		return Update(m, msg)
	case "u", "unpick":
		if launch != nil {
			launch.Queue.Remove(arg)
		}
		return Update(m, UnpickMsg{Number: arg})
	default:
		return m
	}
}

// titleOf returns num's title from m.All, or "" when the backlog hasn't
// (yet) loaded it — Pick still promotes and queues by number alone.
func titleOf(m Model, num string) string {
	for _, iss := range m.All {
		if iss.Number == num {
			return iss.Title
		}
	}
	return ""
}
