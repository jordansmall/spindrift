package main

import (
	"fmt"
	"os"
	"strings"
)

// argvShape describes how a Driver's argv is assembled from the pieces
// driver-exec has on hand -- the prompt, model, effort, --agents JSON, and
// session pin/resume flags (ADR 0009) -- as pure data, so adding a new
// Driver is a data row, not an args.go edit.
type argvShape struct {
	promptStyle    string // "flag" or "positional"
	promptFlag     string // meaningful only when promptStyle == "flag"
	modelFlag      string
	modelOmitEmpty bool     // when true, omit the model slot entirely if model == ""
	agentsFlag     string   // "" means this Driver has no --agents equivalent
	effortFlag     string   // the effort slot is always omitted when effort == ""
	order          []string // permutation of {"prompt","model","agents","session","driverFlags","effort"}
}

// driverInput is the file-path/flag data driver-exec assembles into the
// Driver's own argv (ADR 0009): the prompt, --agents JSON, and session
// pin/resume flags all cross into this process as files (issue #626),
// replacing the shell temp-file/eval marshalling that used to cross the
// devShell boundary. shape carries the Driver's argv layout as data (issue
// #2534), so buildDriverArgs itself carries no per-Driver knowledge.
type driverInput struct {
	shape       argvShape
	promptFile  string
	model       string
	effort      string
	agentsFile  string
	sessionFile string
	driverFlags string
}

// buildDriverArgs reads promptFile (and, if set, agentsFile/sessionFile) and
// assembles the Driver's argv by walking in.shape.order, applying each slot's
// generic rule against the shape data (issue #2534):
//
//   - "prompt": promptFlag then the prompt when promptStyle == "flag";
//     otherwise just the prompt, positional.
//   - "model": modelFlag then model, unless model is empty and
//     modelOmitEmpty is set, in which case the slot is omitted entirely.
//   - "agents": omitted when agentsFlag == "" or agentsFile == ""; otherwise
//     agentsFlag then agentsFile's content, but only when that content is
//     non-empty.
//   - "session": omitted when sessionFile == ""; otherwise sessionFile's
//     content, word-split into separate argv elements (matching the shell's
//     prior `read -ra` word-splitting).
//   - "driverFlags": driverFlags word-split into separate argv elements (may
//     contribute nothing).
//   - "effort": omitted when effort == ""; otherwise effortFlag then effort.
//
// A Driver's shape is entirely a function of its argvShape value -- no
// driver-name conditional appears here, so a new Driver's argv shape is a
// new argvShape data row, not a change to this function.
func buildDriverArgs(in driverInput) ([]string, error) {
	if in.shape.promptStyle != "flag" && in.shape.promptStyle != "positional" {
		return nil, fmt.Errorf("buildDriverArgs: invalid promptStyle %q, want \"flag\" or \"positional\"", in.shape.promptStyle)
	}

	prompt, err := os.ReadFile(in.promptFile)
	if err != nil {
		return nil, err
	}

	var args []string
	for _, slot := range in.shape.order {
		switch slot {
		case "prompt":
			if in.shape.promptStyle == "flag" {
				args = append(args, in.shape.promptFlag, string(prompt))
			} else {
				args = append(args, string(prompt))
			}
		case "model":
			if in.model != "" || !in.shape.modelOmitEmpty {
				args = append(args, in.shape.modelFlag, in.model)
			}
		case "agents":
			if in.shape.agentsFlag == "" || in.agentsFile == "" {
				continue
			}
			agents, err := os.ReadFile(in.agentsFile)
			if err != nil {
				return nil, err
			}
			if len(agents) > 0 {
				args = append(args, in.shape.agentsFlag, string(agents))
			}
		case "session":
			if in.sessionFile == "" {
				continue
			}
			session, err := os.ReadFile(in.sessionFile)
			if err != nil {
				return nil, err
			}
			args = append(args, strings.Fields(string(session))...)
		case "driverFlags":
			args = append(args, strings.Fields(in.driverFlags)...)
		case "effort":
			if in.effort == "" {
				continue
			}
			args = append(args, in.shape.effortFlag, in.effort)
		default:
			return nil, fmt.Errorf("buildDriverArgs: unrecognised argv slot %q", slot)
		}
	}
	return args, nil
}
