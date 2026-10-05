package promptassembly

import (
	"flag"
	"strconv"
	"strings"
)

// AssemblyFlags are the assembly and Passthrough inputs box and the
// assemble-prompt verb both take as flags (issue #4434); BindFlags is the one
// place their names, defaults and parsing live.
type AssemblyFlags struct {
	RegistryFile                string
	ValidateMarkersFile         string
	PromptsDir                  string
	AgentsPromptFiles           string
	DriverAgentFilesDir         string
	CommsContractFile           string
	CheckContractFile           string
	OutcomeContractFile         string
	ResearchOutcomeContractFile string
	Passthrough                 Passthrough
}

// BindFlags registers the shared flag set on fs, parsing straight into a. Flags
// the two callers differ on (devshell, slice and review-round caps, outputs,
// skills) stay with the callers.
func (a *AssemblyFlags) BindFlags(fs *flag.FlagSet) {
	fs.StringVar(&a.RegistryFile, "registry", "", "path to the fragment registry JSON file (required)")
	fs.StringVar(&a.ValidateMarkersFile, "validate-markers-registry", "", "path to the prompt-contract validateMarkers registry JSON file (required)")
	fs.StringVar(&a.PromptsDir, "prompts-dir", "", "PROMPTS_DIR")
	fs.StringVar(&a.AgentsPromptFiles, "agents-prompt-files", "", "nix-baked agent-name -> promptFile JSON map")
	fs.StringVar(&a.DriverAgentFilesDir, "driver-agent-files-dir", "", "opencode-style baked agent files dir, empty for claude")
	fs.StringVar(&a.CommsContractFile, "comms-contract-file", "", "COMMS_CONTRACT_FILE")
	fs.StringVar(&a.CheckContractFile, "check-contract-file", "", "CHECK_CONTRACT_FILE")
	fs.StringVar(&a.OutcomeContractFile, "outcome-contract-file", "", "OUTCOME_CONTRACT_FILE")
	fs.StringVar(&a.ResearchOutcomeContractFile, "research-outcome-contract-file", "", "RESEARCH_OUTCOME_CONTRACT_FILE")

	// Assemble never reads the Passthrough flags; they pass straight through
	// into result.Handoff (issue #2975).
	p := &a.Passthrough
	shape := &p.ArgvShape
	fs.StringVar(&shape.PromptStyle, "argv-prompt-style", "flag", "Handoff.ArgvShape.PromptStyle")
	fs.StringVar(&shape.PromptFlag, "argv-prompt-flag", "", "Handoff.ArgvShape.PromptFlag")
	fs.StringVar(&shape.ModelFlag, "argv-model-flag", "--model", "Handoff.ArgvShape.ModelFlag")
	fs.Var((*omitEmptyValue)(&shape.ModelOmitEmpty), "argv-model-omit-empty", "Handoff.ArgvShape.ModelOmitEmpty: bare form is true; with =, empty is false, else a Go bool (1, 0, true)")
	fs.StringVar(&shape.AgentsFlag, "argv-agents-flag", "", "Handoff.ArgvShape.AgentsFlag")
	fs.StringVar(&shape.EffortFlag, "argv-effort-flag", "--effort", "Handoff.ArgvShape.EffortFlag")
	shape.Order = strings.Fields("prompt model agents session driverFlags effort")
	fs.Var((*orderValue)(&shape.Order), "argv-order", "space-separated Handoff.ArgvShape.Order")
	fs.StringVar(&p.Model, "model", "", "Handoff.Model")
	fs.StringVar(&p.Effort, "effort", "", "Handoff.Effort")
	fs.StringVar(&p.Driver, "driver", "claude", "Handoff.Driver")
	fs.StringVar(&p.DriverBin, "driver-bin", "", "Handoff.DriverBin")
	fs.StringVar(&p.DriverFlags, "driver-flags", "", "Handoff.DriverFlags")
	fs.StringVar(&p.HeartbeatLog, "heartbeat-log", "", "Handoff.HeartbeatLog")
	fs.Var((*budgetTokensValue)(&p.Caps.MaxBudgetTokens), "max-budget-tokens", "Handoff.Caps.MaxBudgetTokens")
	fs.Var((*budgetUSDValue)(&p.Caps.MaxBudgetUSD), "max-budget-usd", "Handoff.Caps.MaxBudgetUSD")
}

// ApplyTo copies the contract and prompt-file paths onto env.
func (a AssemblyFlags) ApplyTo(env *Env) {
	env.PromptsDir = a.PromptsDir
	env.AgentsPromptFiles = a.AgentsPromptFiles
	env.DriverAgentFilesDir = a.DriverAgentFilesDir
	env.CommsContractFile = a.CommsContractFile
	env.CheckContractFile = a.CheckContractFile
	env.OutcomeContractFile = a.OutcomeContractFile
	env.ResearchOutcomeContractFile = a.ResearchOutcomeContractFile
}

// omitEmptyValue accepts both the bare --argv-model-omit-empty and box's
// shim form --argv-model-omit-empty="${DRIVER_ARGV_MODEL_OMIT_EMPTY:-}", whose
// value may be empty (meaning false). As a bool flag, a value must use the =
// form; a space-separated value is left as a positional argument.
type omitEmptyValue bool

func (v *omitEmptyValue) IsBoolFlag() bool { return true }

func (v *omitEmptyValue) String() string {
	if v == nil {
		return "false"
	}
	return strconv.FormatBool(bool(*v))
}

func (v *omitEmptyValue) Set(s string) error {
	if s == "" {
		*v = false
		return nil
	}
	b, err := strconv.ParseBool(s)
	if err != nil {
		return err
	}
	*v = omitEmptyValue(b)
	return nil
}

type orderValue []string

func (v *orderValue) String() string {
	if v == nil {
		return ""
	}
	return strings.Join(*v, " ")
}

func (v *orderValue) Set(s string) error {
	*v = strings.Fields(s)
	return nil
}

// The budget values never return an error from Set: a flag.Int would fail
// fs.Parse on a malformed MAX_BUDGET_* and kill the run (issues #2975, #2694),
// so a bad value degrades to 0 (disabled) instead.
type budgetTokensValue int

func (v *budgetTokensValue) String() string {
	if v == nil {
		return "0"
	}
	return strconv.Itoa(int(*v))
}

func (v *budgetTokensValue) Set(s string) error {
	n, _ := ParseNonnegBudgetTokens(s)
	*v = budgetTokensValue(n)
	return nil
}

type budgetUSDValue float64

func (v *budgetUSDValue) String() string {
	if v == nil {
		return "0"
	}
	return strconv.FormatFloat(float64(*v), 'g', -1, 64)
}

func (v *budgetUSDValue) Set(s string) error {
	n, _ := ParseNonnegBudgetUSD(s)
	*v = budgetUSDValue(n)
	return nil
}
