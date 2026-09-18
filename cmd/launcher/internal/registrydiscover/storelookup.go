package registrydiscover

import (
	"fmt"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/ecosystem"
)

// StoreLookup is the production Lookup for Discover: it reports whether store
// holds a credential for d. It discards the resolved value and never returns
// or logs the credential.
func StoreLookup(store Store, d ecosystem.Declaration) (found bool, err error) {
	cfg, err := storeLookupConfig(store, d)
	if err != nil {
		return false, err
	}

	// Every Peek error means "this store doesn't hold it": credresolver folds a
	// missing file, a missing entry, and an empty value into one error shape.
	// Swallowing err rather than propagating it matches firstMatch's contract that
	// one unreachable store must never abort discovery.
	_, err = credresolver.New(cfg).Peek()
	return err == nil, nil
}

// storeLookupConfig builds the credresolver.Config for store and d from the
// credresolver kind table's own StoreConfig, so the per-format shape is not
// duplicated here.
func storeLookupConfig(store Store, d ecosystem.Declaration) (credresolver.Config, error) {
	kind, ok := credresolver.KindBySourceKey(store.Name)
	if !ok || kind.StoreConfig == nil {
		return credresolver.Config{}, fmt.Errorf("registrydiscover: unknown store %q", store.Name)
	}
	return kind.StoreConfig(store.Path, declarationFacts(d)), nil
}
