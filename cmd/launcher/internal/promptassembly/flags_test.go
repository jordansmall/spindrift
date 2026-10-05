package promptassembly

import (
	"flag"
	"io"
	"reflect"
	"sort"
	"testing"
)

func parseAssemblyFlags(t *testing.T, args ...string) (AssemblyFlags, error) {
	t.Helper()
	var a AssemblyFlags
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	a.BindFlags(fs)
	return a, fs.Parse(args)
}

func TestBindFlagsDefaults(t *testing.T) {
	a, err := parseAssemblyFlags(t)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := AssemblyFlags{
		Passthrough: Passthrough{
			Driver: "claude",
			ArgvShape: ArgvShape{
				PromptStyle: "flag",
				ModelFlag:   "--model",
				EffortFlag:  "--effort",
				Order:       []string{"prompt", "model", "agents", "session", "driverFlags", "effort"},
			},
		},
	}
	if !reflect.DeepEqual(a, want) {
		t.Errorf("defaults = %+v, want %+v", a, want)
	}
}

func TestBindFlagsRegistersExactlyTheSharedSet(t *testing.T) {
	var a AssemblyFlags
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	a.BindFlags(fs)
	var got []string
	fs.VisitAll(func(f *flag.Flag) { got = append(got, f.Name) })
	sort.Strings(got)
	want := []string{
		"agents-prompt-files", "argv-agents-flag", "argv-effort-flag", "argv-model-flag",
		"argv-model-omit-empty", "argv-order", "argv-prompt-flag", "argv-prompt-style",
		"check-contract-file", "comms-contract-file", "driver", "driver-agent-files-dir",
		"driver-bin", "driver-flags", "effort", "heartbeat-log", "max-budget-tokens",
		"max-budget-usd", "model", "outcome-contract-file", "prompts-dir", "registry",
		"research-outcome-contract-file", "validate-markers-registry",
	}
	if len(want) != 24 {
		t.Fatalf("test table has %d names, want 24", len(want))
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("registered flags = %v, want %v", got, want)
	}
}

func TestBindFlagsParsesValues(t *testing.T) {
	a, err := parseAssemblyFlags(t,
		"--registry=r.json", "--validate-markers-registry=v.json", "--prompts-dir=/p",
		"--agents-prompt-files=m.json", "--driver-agent-files-dir=/d",
		"--comms-contract-file=c", "--check-contract-file=k",
		"--outcome-contract-file=o", "--research-outcome-contract-file=ro",
		"--argv-prompt-style=positional", "--argv-prompt-flag=-p",
		"--argv-model-flag=-m", "--argv-agents-flag=--agents", "--argv-effort-flag=-e",
		"--model=opus", "--effort=high", "--driver=opencode", "--driver-bin=/bin/oc",
		"--driver-flags=--x", "--heartbeat-log=/hb",
	)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := AssemblyFlags{
		RegistryFile:                "r.json",
		ValidateMarkersFile:         "v.json",
		PromptsDir:                  "/p",
		AgentsPromptFiles:           "m.json",
		DriverAgentFilesDir:         "/d",
		CommsContractFile:           "c",
		CheckContractFile:           "k",
		OutcomeContractFile:         "o",
		ResearchOutcomeContractFile: "ro",
		Passthrough: Passthrough{
			Model:        "opus",
			Effort:       "high",
			Driver:       "opencode",
			DriverBin:    "/bin/oc",
			DriverFlags:  "--x",
			HeartbeatLog: "/hb",
			ArgvShape: ArgvShape{
				PromptStyle: "positional",
				PromptFlag:  "-p",
				ModelFlag:   "-m",
				AgentsFlag:  "--agents",
				EffortFlag:  "-e",
				Order:       []string{"prompt", "model", "agents", "session", "driverFlags", "effort"},
			},
		},
	}
	if !reflect.DeepEqual(a, want) {
		t.Errorf("parsed = %+v, want %+v", a, want)
	}
}

func TestBindFlagsArgvModelOmitEmpty(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    bool
		wantErr bool
	}{
		{"absent", nil, false, false},
		{"empty", []string{"--argv-model-omit-empty="}, false, false},
		{"zero", []string{"--argv-model-omit-empty=0"}, false, false},
		{"one", []string{"--argv-model-omit-empty=1"}, true, false},
		{"true", []string{"--argv-model-omit-empty=true"}, true, false},
		{"bare", []string{"--argv-model-omit-empty"}, true, false},
		{"garbage", []string{"--argv-model-omit-empty=maybe"}, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, err := parseAssemblyFlags(t, tc.args...)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Parse err = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && a.Passthrough.ArgvShape.ModelOmitEmpty != tc.want {
				t.Errorf("ModelOmitEmpty = %v, want %v", a.Passthrough.ArgvShape.ModelOmitEmpty, tc.want)
			}
		})
	}
}

func TestBindFlagsBudgetsDegradeToZeroWithoutFailingParse(t *testing.T) {
	tests := []struct {
		name       string
		tokens     string
		usd        string
		wantTokens int
		wantUSD    float64
	}{
		{"valid", "1000", "2.5", 1000, 2.5},
		{"malformed", "lots", "lots", 0, 0},
		{"negative", "-3", "-3", 0, 0},
		{"empty", "", "", 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, err := parseAssemblyFlags(t, "--max-budget-tokens="+tc.tokens, "--max-budget-usd="+tc.usd)
			if err != nil {
				t.Fatalf("Parse must not fail on a budget value: %v", err)
			}
			caps := a.Passthrough.Caps
			if caps.MaxBudgetTokens != tc.wantTokens || caps.MaxBudgetUSD != tc.wantUSD {
				t.Errorf("caps = %+v, want tokens %d usd %v", caps, tc.wantTokens, tc.wantUSD)
			}
		})
	}
}

func TestBindFlagsBudgetDefValueIsZero(t *testing.T) {
	var a AssemblyFlags
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	a.BindFlags(fs)
	for _, name := range []string{"max-budget-tokens", "max-budget-usd"} {
		if got := fs.Lookup(name).DefValue; got != "0" {
			t.Errorf("%s DefValue = %q, want \"0\"", name, got)
		}
	}
}

func TestBindFlagsArgvOrderSplitsOnWhitespace(t *testing.T) {
	a, err := parseAssemblyFlags(t, "--argv-order=  prompt   model\tsession ")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"prompt", "model", "session"}
	if got := a.Passthrough.ArgvShape.Order; !reflect.DeepEqual(got, want) {
		t.Errorf("Order = %v, want %v", got, want)
	}
}

func TestApplyToCopiesContractFields(t *testing.T) {
	a := AssemblyFlags{
		PromptsDir:                  "pd",
		AgentsPromptFiles:           "apf",
		DriverAgentFilesDir:         "dafd",
		CommsContractFile:           "comms",
		CheckContractFile:           "check",
		OutcomeContractFile:         "outcome",
		ResearchOutcomeContractFile: "research",
	}
	var env Env
	a.ApplyTo(&env)
	got := []string{env.PromptsDir, env.AgentsPromptFiles, env.DriverAgentFilesDir, env.CommsContractFile, env.CheckContractFile, env.OutcomeContractFile, env.ResearchOutcomeContractFile}
	want := []string{"pd", "apf", "dafd", "comms", "check", "outcome", "research"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ApplyTo env fields = %v, want %v", got, want)
	}
}
