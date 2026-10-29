package promptassembly

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/driver"
)

// Passthrough is the Handoff fields Assemble never sets: the caller layers
// them on once Assemble and Validate both succeed (issue #2975). Issue is not
// here -- it comes from Env.IssueNumber (issue #2979).
type Passthrough struct {
	Model        string
	Effort       string
	Driver       string
	DriverBin    string
	DriverFlags  string
	Devshell     bool
	DevshellName string
	HeartbeatLog string
	ArgvShape    ArgvShape
	Caps         Caps
}

// OutputPaths names the files WriteAssembly writes. ReviewPrompt and
// Fragments are optional: empty skips the file.
type OutputPaths struct {
	Prompt       string
	AgentsJSON   string
	Handoff      string
	ReviewPrompt string
	Fragments    string
}

// ValidateError marks a Validate rejection, which callers print bare: the
// marker message is already operator-facing, unlike an I/O or render failure.
type ValidateError struct{ Err error }

func (e *ValidateError) Error() string { return e.Err.Error() }
func (e *ValidateError) Unwrap() error { return e.Err }

// WriteAssembly runs Assemble then Validate (warnings go to w) and writes the
// prompt, agents JSON, optional review prompt and fragments, and finally the
// Handoff JSON. Handoff is written last so a failure earlier never leaves a
// handoff pointing at files that were not written. The returned Result carries
// the layered Handoff.
func WriteAssembly(env Env, reg Registry, markers []ValidateMarkerRow, p Passthrough, out OutputPaths, w io.Writer) (Result, error) {
	result, err := Assemble(env, reg)
	if err != nil {
		return result, err
	}

	warnings, err := Validate(env, result, markers)
	for _, warning := range warnings {
		fmt.Fprintln(w, warning)
	}
	if err != nil {
		return result, &ValidateError{Err: err}
	}

	if err := os.WriteFile(out.Prompt, []byte(result.Prompt), 0o644); err != nil {
		return result, fmt.Errorf("write prompt output: %w", err)
	}
	if err := os.WriteFile(out.AgentsJSON, []byte(result.AgentsJSON), 0o644); err != nil {
		return result, fmt.Errorf("write agents json output: %w", err)
	}

	result.Handoff.PromptFile = out.Prompt
	if result.AgentsJSON != "" {
		result.Handoff.AgentsFile = out.AgentsJSON
	}
	result.Handoff.Model = p.Model
	result.Handoff.Effort = p.Effort
	result.Handoff.Driver = p.Driver
	// A Driver whose effort rides on the model lets its effort flag beat a
	// model-suffix level, so a REVIEW_MODEL level suffix must set the reviewer
	// effort too, unless REVIEW_EFFORT is explicit (issue #5625).
	if env.ReviewEffortOverride == "" {
		if level, ok := effortSuffixLevel(env.ReviewModelOverride, p.Driver); ok {
			result.Handoff.ReviewEffort = level
		}
	}
	result.Handoff.DriverBin = p.DriverBin
	result.Handoff.DriverFlags = p.DriverFlags
	result.Handoff.Devshell = p.Devshell
	result.Handoff.DevshellName = p.DevshellName
	result.Handoff.Issue = env.IssueNumber
	result.Handoff.HeartbeatLog = p.HeartbeatLog
	result.Handoff.ArgvShape = p.ArgvShape
	result.Handoff.Caps = p.Caps

	if result.ReviewPromptText != "" && out.ReviewPrompt != "" {
		if err := os.WriteFile(out.ReviewPrompt, []byte(result.ReviewPromptText), 0o644); err != nil {
			return result, fmt.Errorf("write review prompt output: %w", err)
		}
		result.Handoff.ReviewPromptFile = out.ReviewPrompt
	}

	if out.Fragments != "" {
		var sb strings.Builder
		for _, name := range result.Fragments {
			sb.WriteString(name)
			sb.WriteByte('\n')
		}
		if err := os.WriteFile(out.Fragments, []byte(sb.String()), 0o644); err != nil {
			return result, fmt.Errorf("write fragments output: %w", err)
		}
	}

	handoffJSON, err := json.Marshal(result.Handoff)
	if err != nil {
		return result, fmt.Errorf("marshal handoff: %w", err)
	}
	if err := os.WriteFile(out.Handoff, handoffJSON, 0o644); err != nil {
		return result, fmt.Errorf("write handoff output: %w", err)
	}
	return result, nil
}

// ScanSkillsFound lists, ", "-joined in byte-sorted order, the skills in dir
// that SkillBaked counts (human-only ones are dropped), for SKILLS_FOUND. A
// flat <name>.md never counts; a missing dir yields "".
func ScanSkillsFound(dir string) string {
	entries, err := os.ReadDir(dir) // sorted by filename
	if err != nil {
		return ""
	}
	var found []string
	for _, e := range entries {
		// bash's `*` glob skips dot-entries.
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if SkillBaked(dir, e.Name()) {
			found = append(found, e.Name())
		}
	}
	return strings.Join(found, ", ")
}

// effortSuffixLevel returns the trailing ":<level>" of model when level is one
// of the Driver's effort-on-model levels, splitting at the last colon like
// lib/effort-on-model.nix.
func effortSuffixLevel(model, driverName string) (string, bool) {
	i := strings.LastIndexByte(model, ':')
	if i < 0 {
		return "", false
	}
	level := model[i+1:]
	return level, slices.Contains(driver.EffortOnModelLevels(driverName), level)
}
