package driver

import (
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
