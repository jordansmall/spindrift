package driver

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// TestNewUnknownDriverErrors verifies that an unrecognized DRIVER value is
// rejected rather than silently falling back.
func TestNewUnknownDriverErrors(t *testing.T) {
	t.Parallel()
	if _, err := NewSet().New("bogus"); err == nil {
		t.Fatal("New(\"bogus\"): expected error, got nil")
	}
}

// TestProviderNamespacedModels pins the Go mirror of the Nix
// providerNamespacedModels fact, including names New would reject.
func TestProviderNamespacedModels(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		"opencode": true,
		"pi":       true,
		"claude":   false,
		"":         false,
		"nope":     false,
	} {
		if got := NewSet().ProviderNamespacedModels(name); got != want {
			t.Errorf("ProviderNamespacedModels(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestEffortOnModelLevels pins the Go mirror of the Nix effortOnModelLevels
// fact: only a Driver that reads effort from a model id's `:<level>` suffix
// carries levels (issue #5625).
func TestEffortOnModelLevels(t *testing.T) {
	t.Parallel()
	want := []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}
	if got := NewSet().EffortOnModelLevels("pi"); !slices.Equal(got, want) {
		t.Errorf("EffortOnModelLevels(pi) = %v, want %v", got, want)
	}
	for _, name := range []string{"claude", "opencode", "", "nope"} {
		if got := NewSet().EffortOnModelLevels(name); got != nil {
			t.Errorf("EffortOnModelLevels(%q) = %v, want nil", name, got)
		}
	}
}

func TestCredentialRulesClaudeAlwaysApplies(t *testing.T) {
	t.Parallel()
	rules, ok := NewSet().CredentialRules("claude")
	if !ok || len(rules) != 1 {
		t.Fatalf("CredentialRules(claude) = %v, %v; want one rule", rules, ok)
	}
	r := rules[0]
	if want := []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}; !slices.Equal(r.Env, want) {
		t.Errorf("Env = %v, want %v", r.Env, want)
	}
	if want := []string{"claude", "setup-token"}; !slices.Equal(r.SetupCommand, want) {
		t.Errorf("SetupCommand = %v, want %v", r.SetupCommand, want)
	}
	if !r.AppliesTo() || !r.AppliesTo("claude-sonnet-4-5") {
		t.Error("claude rule must apply regardless of models")
	}
}

func TestCredentialRulesOpencodeAppliesOnlyToCopilot(t *testing.T) {
	t.Parallel()
	rules, ok := NewSet().CredentialRules("opencode")
	if !ok || len(rules) != 1 {
		t.Fatalf("CredentialRules(opencode) = %v, %v; want one rule", rules, ok)
	}
	r := rules[0]
	if want := []string{"OPENCODE_AUTH_CONTENT"}; !slices.Equal(r.Env, want) {
		t.Errorf("Env = %v, want %v", r.Env, want)
	}
	if r.AppliesTo() || r.AppliesTo("anthropic/claude", "openai/gpt") {
		t.Error("opencode rule must not apply without a github-copilot model")
	}
	if !r.AppliesTo("anthropic/claude", "github-copilot/gpt-5") {
		t.Error("opencode rule must apply when any model is github-copilot")
	}
}

func TestCredentialRulesPiOnePerProvider(t *testing.T) {
	t.Parallel()
	rules, ok := NewSet().CredentialRules("pi")
	if !ok {
		t.Fatal("CredentialRules(pi) not found")
	}
	want := map[string]string{
		"anthropic":      "ANTHROPIC_API_KEY",
		"openai":         "OPENAI_API_KEY",
		"google":         "GEMINI_API_KEY",
		"openrouter":     "OPENROUTER_API_KEY",
		"groq":           "GROQ_API_KEY",
		"xai":            "XAI_API_KEY",
		"mistral":        "MISTRAL_API_KEY",
		"deepseek":       "DEEPSEEK_API_KEY",
		"github-copilot": "PI_AUTH_CONTENT",
	}
	if len(rules) != len(want) {
		t.Fatalf("CredentialRules(pi) has %d rules, want %d", len(rules), len(want))
	}
	for provider, env := range want {
		model := provider + "/some-model"
		var hit []CredentialRule
		for _, r := range rules {
			if r.AppliesTo(model) {
				hit = append(hit, r)
			}
		}
		if len(hit) != 1 || !slices.Equal(hit[0].Env, []string{env}) {
			t.Errorf("%s: rules applying = %v, want exactly one over %s", model, hit, env)
			continue
		}
		if len(hit[0].SetupCommand) != 0 {
			t.Errorf("%s: SetupCommand = %v, want none", provider, hit[0].SetupCommand)
		}
		if !strings.Contains(hit[0].Remedy, env) {
			t.Errorf("%s: remedy %q does not name %s", provider, hit[0].Remedy, env)
		}
	}
	for _, r := range rules {
		if r.AppliesTo() || r.AppliesTo("bare-model", "cohere/command") {
			t.Errorf("rule over %v must not apply without its Provider's model", r.Env)
		}
	}
}

func TestCredentialRuleSatisfied(t *testing.T) {
	t.Parallel()
	r := CredentialRule{Env: []string{"A", "B"}}
	for _, tc := range []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"none set", nil, false},
		{"empty value", map[string]string{"A": ""}, false},
		{"first set", map[string]string{"A": "x"}, true},
		{"later set", map[string]string{"B": "x"}, true},
		{"unrelated set", map[string]string{"C": "x"}, false},
	} {
		if got := r.Satisfied(func(k string) string { return tc.env[k] }); got != tc.want {
			t.Errorf("%s: Satisfied = %v, want %v", tc.name, got, tc.want)
		}
	}
	if (CredentialRule{}).Satisfied(func(string) string { return "x" }) {
		t.Error("a rule with no env vars must not be satisfied")
	}
}

func TestCredentialRulesEmptyNameIsClaude(t *testing.T) {
	t.Parallel()
	got, ok := NewSet().CredentialRules("")
	want, _ := NewSet().CredentialRules("claude")
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("CredentialRules(\"\") = %v, %v; want claude's %v", got, ok, want)
	}
}

func TestCredentialRulesUnknownDriver(t *testing.T) {
	t.Parallel()
	if rules, ok := NewSet().CredentialRules("nope"); ok || rules != nil {
		t.Errorf("CredentialRules(nope) = %v, %v; want nil, false", rules, ok)
	}
}

func TestAddConnectorPanicsOnLazyDuplicate(t *testing.T) {
	t.Parallel()
	s := NewSet()
	s.AddConnector("lazydup", nil)
	defer func() {
		if recover() == nil {
			t.Error("AddConnector of a lazily added name did not panic")
		}
	}()
	s.AddConnector("lazydup", nil)
}

func TestNewSetPanicsOnDuplicateNative(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("NewSet of a repeated native name did not panic")
		}
	}()
	NewSet(driverkit.Strategy{Name: "twice"}, driverkit.Strategy{Name: "twice"})
}

// Each Set owns its Drivers, Descriptors and shadows.
func TestSetsAreIndependent(t *testing.T) {
	t.Parallel()
	a, b := NewSet(driverkit.Strategy{Name: "only-a"}), NewSet()
	a.AddConnector("lazy-a", nil)
	a.UseDescriptor(Descriptor{Name: "claude", ProviderNamespacedModels: true})
	a.UseDescriptor(Descriptor{Name: "added"})
	a.UseShadow("only-a", "m.wasm")
	if b.Has("only-a") || b.Has("lazy-a") {
		t.Error("a Driver added to one Set is in another")
	}
	if b.ProviderNamespacedModels("claude") {
		t.Error("a Descriptor replaced in one Set is replaced in another")
	}
	if _, ok := b.DescriptorFor("added"); ok {
		t.Error("a Descriptor added to one Set is in another")
	}
	if _, ok := b.ShadowModule("only-a"); ok {
		t.Error("a shadow recorded in one Set is in another")
	}
}

func TestUsePrimaryReplacesNativeInNew(t *testing.T) {
	t.Parallel()
	s := NewSet(driverkit.Strategy{Name: "prim"})
	s.UsePrimary("prim", func() (driverkit.Strategy, error) {
		return driverkit.Strategy{Name: "prim", SessionFlags: func(mode, _, _, _ string) (driverkit.SessionFlag, error) {
			return driverkit.SessionFlag{Flag: "--from-connector", Value: mode}, nil
		}}, nil
	})
	d, err := s.New("prim")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := d.SessionFlags("initial", "", "", ""); err != nil || got != (driverkit.SessionFlag{Flag: "--from-connector", Value: "initial"}) {
		t.Errorf("SessionFlags = %+v, %v; want the Connector's", got, err)
	}
	if !s.HasNative("prim") {
		t.Error("HasNative(prim) = false, want the linked-in Strategy kept")
	}
	if _, ok := s.NativeStrategy("prim"); !ok {
		t.Error("NativeStrategy(prim) not ok, want the linked-in Strategy kept")
	}
	if n := slices.Index(s.Names(), "prim"); n < 0 || slices.Index(s.Names()[n+1:], "prim") >= 0 {
		t.Errorf("Names() = %v, want prim listed once", s.Names())
	}
}

func TestUsePrimaryLoadFailureNamesDriverAndDoesNotFallBack(t *testing.T) {
	t.Parallel()
	s := NewSet(driverkit.Strategy{Name: "prim"})
	s.UsePrimary("prim", func() (driverkit.Strategy, error) { return driverkit.Strategy{}, errors.New("boom") })
	d, err := s.New("prim")
	if err == nil || !strings.Contains(err.Error(), `DRIVER "prim"`) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("New(prim) = %v, %v, want an error naming DRIVER \"prim\"", d, err)
	}
}

func TestUsePrimaryPanicsWithoutNativeOrOnRepeat(t *testing.T) {
	t.Parallel()
	s := NewSet(driverkit.Strategy{Name: "twice"})
	load := func() (driverkit.Strategy, error) { return driverkit.Strategy{}, nil }
	mustPanic := func(what string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s did not panic", what)
			}
		}()
		f()
	}
	mustPanic("UsePrimary without a native Strategy", func() { s.UsePrimary("nonative", load) })
	s.AddConnector("lazyonly", load)
	mustPanic("UsePrimary of a lazy-only name", func() { s.UsePrimary("lazyonly", load) })
	s.UsePrimary("twice", load)
	mustPanic("a repeated UsePrimary", func() { s.UsePrimary("twice", load) })
}

// A Descriptor without a registered Driver is a Connector-only Driver whose
// row never arrived; the error says so, not "unknown".
func TestNewDescriptorWithoutDriverNamesTheMissingRow(t *testing.T) {
	t.Parallel()
	s := NewSet()
	s.UseDescriptor(Descriptor{Name: "rowless"})
	for _, name := range []string{"rowless", "claude"} {
		want := `DRIVER "` + name + `": no Driver row supplies its Connector module`
		if _, err := s.New(name); err == nil || err.Error() != want {
			t.Errorf("New(%q) = %v, want %q", name, err, want)
		}
	}
	if _, err := s.New("bogus"); err == nil || !strings.Contains(err.Error(), "unknown DRIVER") {
		t.Errorf("New(bogus) = %v, want the unknown-DRIVER error", err)
	}
}

func TestSessionArgs(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		driver  string
		answer  driverkit.SessionFlag
		want    []string
		wantErr string
	}{
		{"zero answer renders nothing", "claude", driverkit.SessionFlag{}, nil, ""},
		{"declared flag and value", "claude", driverkit.SessionFlag{Flag: "--session-id", Value: "u-1"}, []string{"--session-id", "u-1"}, ""},
		{"second declared flag", "opencode", driverkit.SessionFlag{Flag: "--session", Value: "ses_x"}, []string{"--session", "ses_x"}, ""},
		{"value with spaces is one word", "opencode", driverkit.SessionFlag{Flag: "--title", Value: "spindrift-o/r 7 x"}, []string{"--title", "spindrift-o/r 7 x"}, ""},
		{"undeclared flag", "claude", driverkit.SessionFlag{Flag: "--settings", Value: "x"}, nil, `driver "claude" answered session flag "--settings"`},
		{"another Driver's flag", "claude", driverkit.SessionFlag{Flag: "--title", Value: "x"}, nil, `session flag "--title"`},
		{"dash-led value is refused", "opencode", driverkit.SessionFlag{Flag: "--title", Value: "--model=evil/x"}, nil, `driver "opencode" answered session flag "--title" with value "--model=evil/x"`},
		{"bare dash value is refused", "claude", driverkit.SessionFlag{Flag: "--session-id", Value: "-x"}, nil, `with value "-x"`},
		{"unknown Driver has no row", "bogus", driverkit.SessionFlag{Flag: "--session-id", Value: "x"}, nil, `driver "bogus" has no row declaring session flags`},
		{"value with no flag", "claude", driverkit.SessionFlag{Value: "--settings"}, nil, `driver "claude" answered session value "--settings" with no flag`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := NewSet().SessionArgs(c.driver, c.answer)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("SessionArgs error = %v, want it to contain %q", err, c.wantErr)
				}
				return
			}
			if err != nil || !slices.Equal(got, c.want) {
				t.Errorf("SessionArgs = %q, %v; want %q", got, err, c.want)
			}
		})
	}
}
