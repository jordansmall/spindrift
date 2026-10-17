package driver

// Descriptor is the Go mirror of a lib/drivers/*.nix registry row's shared
// facts, rendered into descriptor_gen.go by `nix run .#regen` (issue #5503).
type Descriptor struct {
	// Name is the Driver identifier, the key of the Nix registry row.
	Name string
	// ProviderNamespacedModels is set when the Driver's model ids are
	// <provider>/<model>.
	ProviderNamespacedModels bool
}
