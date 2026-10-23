package driver

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Descriptor is the Go mirror of a lib/drivers/*.nix registry row's shared
// facts, rendered into descriptor_gen.go by `nix run .#regen` (issue #5503).
type Descriptor struct {
	// Name is the Driver identifier, the key of the Nix registry row.
	Name string
	// ProviderNamespacedModels is set when the Driver's model ids are
	// <provider>/<model>.
	ProviderNamespacedModels bool
	// ClosedCredentials is set when Credentials name every Provider the
	// Driver can authenticate from env, so a model whose <provider>/ prefix
	// matches no rule is authenticated only by an AuthContent entry for that
	// Provider.
	ClosedCredentials bool
	// Credentials are the rules by which the Driver is authenticated; a run
	// is authenticated when every rule that applies to its models is met.
	Credentials []CredentialRule
	// AuthContent constrains the Provider-keyed auth store a Driver reads from
	// an env var; nil when the Driver has none.
	AuthContent *AuthContent
}

// CredentialRule is one way a Driver is authenticated. It is plain data
// (ADR 0066 decision 7) so it can travel in the launcher input document
// (ADR 0020) should the registry ever open.
type CredentialRule struct {
	// Env lists the env vars that satisfy the rule, either-or, in fetch order.
	Env []string
	// ModelPrefix gates the rule to runs using a model with this prefix;
	// empty means the rule always applies.
	ModelPrefix string
	// Remedy is shown when the rule is unmet.
	Remedy string
	// SetupCommand is the argv that obtains the credential for Env[0]; empty
	// when the Driver offers none.
	SetupCommand []string
	// Labels is the human label quickstart's fallback prompt shows for an env
	// var after Env[0], keyed by name; a var without one is prompted as
	// "Value for NAME".
	Labels map[string]string
	// SetupHint is a short note shown beside the setup command offer, e.g. how
	// it authenticates; empty when none.
	SetupHint string
}

// Satisfied reports whether any of the rule's env vars has a non-empty value
// under lookup.
func (r CredentialRule) Satisfied(lookup func(string) string) bool {
	for _, env := range r.Env {
		if lookup(env) != "" {
			return true
		}
	}
	return false
}

// AppliesTo reports whether the rule is in force for a run using these models.
func (r CredentialRule) AppliesTo(models ...string) bool {
	if r.ModelPrefix == "" {
		return true
	}
	for _, m := range models {
		if strings.HasPrefix(m, r.ModelPrefix) {
			return true
		}
	}
	return false
}

// DescriptorNames returns the sorted names of the generated Descriptors, one
// per Driver in the Nix registry. A binary that links the bundle registers
// exactly this set.
func DescriptorNames() []string {
	names := make([]string, 0, len(descriptors))
	for _, d := range descriptors {
		names = append(names, d.Name)
	}
	sort.Strings(names)
	return names
}

// CheckClosedCredentials fails, for a Driver whose Descriptor sets
// ClosedCredentials, every Provider that a model in models names but that is
// authenticated by neither a credential rule nor an api_key entry in the
// Driver's auth store (read through lookup), and every model with an empty
// Provider. Without it such a model would pass the credential check and fail
// only mid-run. Empty and bare (no "/") models are skipped; a Driver without
// the fact stays open-ended and always returns nil. An unparseable store is
// left to AuthContent.Check to report.
func CheckClosedCredentials(name string, models []string, lookup func(string) string) error {
	d, found := descriptor(orDefault(name))
	if !found || !d.ClosedCredentials {
		return nil
	}
	var prefixes []string
	for _, r := range d.Credentials {
		if r.ModelPrefix != "" && !slices.Contains(prefixes, r.ModelPrefix) {
			prefixes = append(prefixes, r.ModelPrefix)
		}
	}
	var stored map[string]json.RawMessage
	storeEnv := ""
	storeBroken := false
	if d.AuthContent != nil {
		storeEnv = d.AuthContent.Env
		if content := lookup(storeEnv); content != "" {
			var ok bool
			stored, ok = parseStore(content)
			storeBroken = !ok
		}
	}
	var errs []error
	var unknown []string
	for _, m := range models {
		provider, _, ok := strings.Cut(m, "/")
		if !ok {
			continue
		}
		if provider == "" {
			errs = append(errs, fmt.Errorf("model %q names no Provider before the \"/\"; write it as <provider>/<model> with one of the supported prefixes (%s)",
				m, strings.Join(prefixes, ", ")))
			continue
		}
		if storeBroken || slices.Contains(unknown, provider) {
			continue
		}
		matched := slices.ContainsFunc(d.Credentials, func(r CredentialRule) bool { return r.AppliesTo(m) })
		if typ, _ := entryType(stored[provider]); !matched && typ != apiKeyEntryType {
			unknown = append(unknown, provider)
		}
	}
	sort.Strings(unknown)
	for _, p := range unknown {
		remedy := "pick a Provider the " + d.Name + " Driver supports"
		if storeEnv != "" {
			remedy += ", or add an api_key entry for " + p + " to " + storeEnv
		}
		errs = append(errs, fmt.Errorf("no credential covers the %s Provider under the %s Driver (supported prefixes: %s); %s",
			p, d.Name, strings.Join(prefixes, ", "), remedy))
	}
	return errors.Join(errs...)
}
