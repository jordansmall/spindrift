package main

import (
	"fmt"
	"os"
	"strings"
)

// argvShape describes a Driver's argv layout as data (ADR 0009).
type argvShape struct {
	promptStyle    string // "flag" or "positional"
	promptFlag     string // meaningful only when promptStyle == "flag"
	modelFlag      string
	modelOmitEmpty bool     // when true, omit the model slot entirely if model == ""
	agentsFlag     string   // "" means this Driver has no --agents equivalent
	effortFlag     string   // the effort slot is always omitted when effort == ""
	order          []string // permutation of {"prompt","model","agents","session","driverFlags","effort"}
}

// driverInput holds the pieces of a Driver's argv (ADR 0009). The prompt,
// --agents JSON, and session pin/resume flags reach this process as files
// (issue #626); shape carries the argv layout as data (issue #2534).
type driverInput struct {
	shape       argvShape
	promptFile  string
	model       string
	effort      string
	agentsFile  string
	sessionFile string
	driverFlags string
}

// buildDriverArgs reads promptFile (and, when set, agentsFile/sessionFile) and
// assembles the Driver's argv by walking in.shape.order. No driver-name
// conditional appears here: a new Driver's argv shape is a new argvShape data
// row, not a change to this function (issue #2534).
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
