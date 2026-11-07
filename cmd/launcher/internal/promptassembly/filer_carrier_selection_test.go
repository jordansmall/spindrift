package promptassembly

import (
	"fmt"
	"strings"
	"testing"
)

// relayCarrierRegistry carries every signal-carrier and Filer-relay row under
// test with the real gate and var names and one marker body per fragment, so
// each test reads which fragment a gate selected from the marker alone. The
// three FILE ISSUES step bodies splice the triage recap the way the shipped
// ones do.
func relayCarrierRegistry(t *testing.T) (Registry, Env) {
	t.Helper()
	return syntheticRegistry(t,
		[]FragmentRow{
			{Axis: "tracker", Shared: true, Gate: "ISSUE_TRACKER_RELAYED_LOG", Fragment: "research-verdict-relay.md", Var: "RESEARCH_VERDICT_RELAY_STEP"},
			{Axis: "tracker", Shared: true, Gate: "ISSUE_TRACKER_RELAYED_SOCKET", Fragment: "research-verdict-relay-socket.md", Var: "RESEARCH_VERDICT_RELAY_SOCKET_STEP"},
			{Gate: "BOX_ACCESS_READ_ONLY_LOG", Fragment: "open-pr-create-outbox.md", Var: "OPEN_PR_CREATE_READ_ONLY_STEP"},
			{Gate: "BOX_ACCESS_READ_ONLY_SOCKET", Fragment: "open-pr-create-outbox-socket.md", Var: "OPEN_PR_CREATE_READ_ONLY_SOCKET_STEP"},
			{Gate: "PR_FORGE_READ_ONLY_LOG", Fragment: "if-blocked-pr-outbox.md", Var: "IF_BLOCKED_PR_READ_ONLY_STEP"},
			{Gate: "PR_FORGE_READ_ONLY_SOCKET", Fragment: "if-blocked-pr-outbox-socket.md", Var: "IF_BLOCKED_PR_READ_ONLY_SOCKET_STEP"},
			{Gate: "FILER_ENABLED", Fragment: "file-issues-triage-recap.md", Var: "FILE_ISSUES_TRIAGE_RECAP_STEP"},
			{Gate: "FILER_FILE_DIRECT_ANY", Fragment: "file-issues-direct.md", Var: "FILE_ISSUES_DIRECT_STEP"},
			{Gate: "FILER_FILE_RELAY_LOG", Fragment: "file-issues-relay.md", Var: "FILE_ISSUES_RELAY_STEP"},
			{Gate: "FILER_FILE_RELAY_SOCKET", Fragment: "file-issues-relay-socket.md", Var: "FILE_ISSUES_RELAY_SOCKET_STEP"},
			{Gate: "FILER_FILE_RELAY_LOG", Fragment: "research-file-issues-relay.md", Var: "RESEARCH_FILE_ISSUES_RELAY_STEP"},
			{Gate: "FILER_FILE_RELAY_SOCKET", Fragment: "research-file-issues-relay-socket.md", Var: "RESEARCH_FILE_ISSUES_RELAY_SOCKET_STEP"},
			{Axis: "filer", Gate: "FILER_FILE_DIRECT_GH", Fragment: "github/filer-direct-file.md", Var: "FILER_FILE_DIRECT_STEP"},
			{Gate: "FILER_FILE_RELAY_LOG", Fragment: "filer-file-relay.md", Var: "FILER_FILE_RELAY_STEP"},
			{Gate: "FILER_FILE_RELAY_SOCKET", Fragment: "filer-file-relay-socket.md", Var: "FILER_FILE_RELAY_SOCKET_STEP"},
			{Gate: "FILER_FILE_RELAY_WORK", Fragment: "filer-label-relay.md", Var: "FILER_LABEL_RELAY_STEP"},
			{Gate: "FILER_FILE_RELAY_RESEARCH", Fragment: "filer-label-relay-research.md", Var: "FILER_LABEL_RELAY_RESEARCH_STEP"},
		},
		map[string]string{
			"issue-prompt.md": "[${OPEN_PR_CREATE_READ_ONLY_STEP}${OPEN_PR_CREATE_READ_ONLY_SOCKET_STEP}" +
				"${IF_BLOCKED_PR_READ_ONLY_STEP}${IF_BLOCKED_PR_READ_ONLY_SOCKET_STEP}" +
				"${FILE_ISSUES_DIRECT_STEP}${FILE_ISSUES_RELAY_STEP}${FILE_ISSUES_RELAY_SOCKET_STEP}]\n",
			"research-prompt.md": "[${RESEARCH_VERDICT_RELAY_STEP}${RESEARCH_VERDICT_RELAY_SOCKET_STEP}" +
				"${RESEARCH_FILE_ISSUES_RELAY_STEP}${RESEARCH_FILE_ISSUES_RELAY_SOCKET_STEP}${FILER_FILE_DIRECT_STEP}]\n",
			"research-self-contained-prompt.md": "[${RESEARCH_VERDICT_RELAY_STEP}${RESEARCH_VERDICT_RELAY_SOCKET_STEP}" +
				"${RESEARCH_FILE_ISSUES_RELAY_STEP}${RESEARCH_FILE_ISSUES_RELAY_SOCKET_STEP}${FILER_FILE_DIRECT_STEP}]\n",
			"filer-prompt.md": "${FILER_FILE_DIRECT_STEP}${FILER_FILE_RELAY_STEP}${FILER_FILE_RELAY_SOCKET_STEP}" +
				"${FILER_LABEL_RELAY_STEP}${FILER_LABEL_RELAY_RESEARCH_STEP}",

			"fragments/research-verdict-relay.md":            "VERDICT-LOG,",
			"fragments/research-verdict-relay-socket.md":     "VERDICT-SOCKET,",
			"fragments/open-pr-create-outbox.md":             "PR-OPEN-LOG,",
			"fragments/open-pr-create-outbox-socket.md":      "PR-OPEN-SOCKET,",
			"fragments/if-blocked-pr-outbox.md":              "PR-BLOCKED-LOG,",
			"fragments/if-blocked-pr-outbox-socket.md":       "PR-BLOCKED-SOCKET,",
			"fragments/file-issues-triage-recap.md":          "RECAP",
			"fragments/file-issues-direct.md":                "# FILE ISSUES\n\n${FILE_ISSUES_TRIAGE_RECAP_STEP}ISSUES-DIRECT,",
			"fragments/file-issues-relay.md":                 "# FILE ISSUES\n\n${FILE_ISSUES_TRIAGE_RECAP_STEP}ISSUES-LOG,",
			"fragments/file-issues-relay-socket.md":          "# FILE ISSUES\n\n${FILE_ISSUES_TRIAGE_RECAP_STEP}ISSUES-SOCKET,",
			"fragments/research-file-issues-relay.md":        "FINDINGS-LOG,",
			"fragments/research-file-issues-relay-socket.md": "FINDINGS-SOCKET,",
			"fragments/github/filer-direct-file.md":          "DIRECT-FILE,",
			"fragments/filer-file-relay.md":                  "FILER-LOG,",
			"fragments/filer-file-relay-socket.md":           "FILER-SOCKET,",
			"fragments/filer-label-relay.md":                 "WORK-LABEL,",
			"fragments/filer-label-relay-research.md":        "RESEARCH-LABEL,",
		})
}

// filerRoster provisions the filer subagent, whose prompt template is staged
// as filer-prompt.md.
func filerRoster(env *Env) {
	env.AgentsJSONTemplate = `{"filer":{"model":"m"}}`
	env.AgentsPromptFiles = `{"filer":"filer-prompt.md"}`
}

// Issue #3726: each signal channel (research verdict comment, PR intent at
// OPEN A PULL REQUEST and IF BLOCKED, issue intent in the coordinator's and
// the Filer's own prompt) forks on SignalCarrier alone, to exactly one of its
// log or socket fragments. An unset carrier is the log one (issue #4376).
func TestAssembleSignalCarrierSelectsLogOrSocketFragment(t *testing.T) {
	reg, base := relayCarrierRegistry(t)
	filerRoster(&base)

	cases := []struct {
		name         string
		mutate       func(*Env)
		filerPrompt  bool
		logMarker    string
		socketMarker string
	}{
		{"research verdict", func(e *Env) { e.DispatchKind = "research"; e.BoxWriteEnabled = false }, false, "VERDICT-LOG,", "VERDICT-SOCKET,"},
		{"work OPEN A PULL REQUEST", func(e *Env) { e.BoxWriteEnabled = false }, false, "PR-OPEN-LOG,", "PR-OPEN-SOCKET,"},
		{"work IF BLOCKED", func(e *Env) { e.BoxWriteEnabled = false }, false, "PR-BLOCKED-LOG,", "PR-BLOCKED-SOCKET,"},
		{"work issue intent, coordinator", func(e *Env) { e.FilerEnabled = true; e.BoxWriteEnabled = false }, false, "ISSUES-LOG,", "ISSUES-SOCKET,"},
		{"work issue intent, filer", func(e *Env) { e.FilerEnabled = true; e.BoxWriteEnabled = false }, true, "FILER-LOG,", "FILER-SOCKET,"},
	}
	for _, tc := range cases {
		for _, carrier := range []string{"", "log", "socket"} {
			t.Run(fmt.Sprintf("%s/carrier=%q", tc.name, carrier), func(t *testing.T) {
				env := base
				tc.mutate(&env)
				env.SignalCarrier = carrier
				result, err := Assemble(env, reg)
				if err != nil {
					t.Fatalf("Assemble: %v", err)
				}
				got := result.Prompt
				if tc.filerPrompt {
					got = agentPromptFromJSON(t, result.AgentsJSON, "filer")
				}
				want, other := tc.logMarker, tc.socketMarker
				if carrier == "socket" {
					want, other = other, want
				}
				if !strings.Contains(got, want) {
					t.Errorf("prompt = %q, want %q", got, want)
				}
				if strings.Contains(got, other) {
					t.Errorf("prompt = %q, want %q absent", got, other)
				}
			})
		}
	}
}

// Issue #2593 (ADR 0041): a research dispatch with the Filer provisioned
// always relays its findings, with no BoxWriteEnabled condition and never the
// direct-file step, on either carrier and both research bases; without the
// Filer there is no findings step at all.
func TestAssembleResearchFileFindingsRelay(t *testing.T) {
	reg, base := relayCarrierRegistry(t)
	base.DispatchKind = "research"

	for _, selfContained := range []bool{false, true} {
		for _, filer := range []bool{true, false} {
			for _, boxWrite := range []bool{true, false} {
				for _, carrier := range []string{"", "log", "socket"} {
					t.Run(fmt.Sprintf("selfContained=%v/filer=%v/boxWrite=%v/carrier=%q", selfContained, filer, boxWrite, carrier), func(t *testing.T) {
						env := base
						env.SelfContained = selfContained
						env.FilerEnabled = filer
						env.BoxWriteEnabled = boxWrite
						env.SignalCarrier = carrier
						result, err := Assemble(env, reg)
						if err != nil {
							t.Fatalf("Assemble: %v", err)
						}

						want, other := "FINDINGS-LOG,", "FINDINGS-SOCKET,"
						if carrier == "socket" {
							want, other = other, want
						}
						if has := strings.Contains(result.Prompt, want); has != filer {
							t.Errorf("findings step %q rendered = %v, want %v: %q", want, has, filer, result.Prompt)
						}
						if strings.Contains(result.Prompt, other) {
							t.Errorf("prompt contains the other carrier's findings step %q: %q", other, result.Prompt)
						}
						if strings.Contains(result.Prompt, "DIRECT-FILE,") {
							t.Errorf("prompt contains the direct-file step, want never in a research prompt: %q", result.Prompt)
						}
						if strings.Contains(result.Prompt, "${") {
							t.Errorf("prompt contains an unsubstituted ${...} token: %q", result.Prompt)
						}
					})
				}
			}
		}
	}
}

// A review finding on issue #2593: the kind-agnostic FILER_FILE_RELAY gate
// made a research dispatch render the work-worded label sentence, though the
// launcher's research path applies a different label. Each kind takes its own
// label-relay step and a direct-filing Filer takes neither.
func TestAssembleFilerLabelRelayStepByKind(t *testing.T) {
	reg, base := relayCarrierRegistry(t)
	base.FilerEnabled = true

	cases := []struct {
		name   string
		mutate func(*Env)
		want   string
		absent []string
	}{
		{"research + Filer", func(e *Env) { e.DispatchKind = "research"; e.BoxWriteEnabled = true }, "RESEARCH-LABEL,", []string{"WORK-LABEL,"}},
		{"work relay (read-only + Filer)", func(e *Env) { e.BoxWriteEnabled = false }, "WORK-LABEL,", []string{"RESEARCH-LABEL,"}},
		{"work direct (read-write + Filer)", func(e *Env) { e.BoxWriteEnabled = true }, "", []string{"WORK-LABEL,", "RESEARCH-LABEL,"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := base
			tc.mutate(&env)
			got := agentPrompt(t, env, reg, "filer")
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Errorf("filer prompt = %q, want %q", got, tc.want)
			}
			for _, bad := range tc.absent {
				if strings.Contains(got, bad) {
					t.Errorf("filer prompt = %q, want %q absent", got, bad)
				}
			}
			if strings.Contains(got, "${") {
				t.Errorf("filer prompt = %q, want no unsubstituted ${...} token", got)
			}
		})
	}
}

// Issue #6184: the FILE ISSUES triage recap is one fragment the direct, relay
// and relay-socket step fragments splice in, gated on the Filer alone, so
// whichever variant a cell picks carries it exactly once, and no Filer carries
// none.
func TestAssembleWorkPromptFileIssuesTriageRecapPerFilerCell(t *testing.T) {
	reg, base := relayCarrierRegistry(t)

	cases := []struct {
		name        string
		filer       bool
		writable    bool
		carrier     string
		wantVariant string
	}{
		{name: "direct", filer: true, writable: true, wantVariant: "ISSUES-DIRECT,"},
		{name: "relay log", filer: true, carrier: "log", wantVariant: "ISSUES-LOG,"},
		{name: "relay socket", filer: true, carrier: "socket", wantVariant: "ISSUES-SOCKET,"},
		{name: "no filer", writable: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := base
			env.FilerEnabled = tc.filer
			env.BoxWriteEnabled = tc.writable
			env.SignalCarrier = tc.carrier
			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if tc.wantVariant == "" {
				if strings.Contains(result.Prompt, "RECAP") || strings.Contains(result.Prompt, "FILE ISSUES") {
					t.Errorf("prompt = %q, want no FILE ISSUES step with no Filer", result.Prompt)
				}
				return
			}
			if got := strings.Count(result.Prompt, "RECAP"); got != 1 {
				t.Errorf("recap appears %d times, want exactly 1: %q", got, result.Prompt)
			}
			if want := "# FILE ISSUES\n\nRECAP\n\n" + tc.wantVariant; !strings.Contains(result.Prompt, want) {
				t.Errorf("prompt = %q, want the recap spliced into the variant as %q", result.Prompt, want)
			}
		})
	}
}
