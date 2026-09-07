package registrydiscover

import (
	"fmt"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/ecosystem"
)

// StoreLookup is the production Lookup (see Discover): it answers "does this
// store hold a credential for the declaration" by delegating to credresolver,
// discarding the resolved value. It never returns or logs the credential.
func StoreLookup(store Store, d ecosystem.Declaration) (found bool, err error) {
	cfg, err := storeLookupConfig(store, d)
	if err != nil {
		return false, err
	}

	// Every Peek error -- missing file, host/registry/key not present, an
	// empty value -- means "this store doesn't hold it", the ordinary case
	// discovery runs into for every store that isn't the match. credresolver
	// folds a missing file and a missing entry into the same error shape, so
	// there is nothing more specific to distinguish here; treating all of
	// them as not-found (rather than propagating err) matches firstMatch's
	// own "one unreachable store must never abort discovery" contract.
	_, err = credresolver.New(cfg).Peek()
	return err == nil, nil
}

// storeLookupConfig maps a Store and Declaration onto the credresolver.Config
// that answers whether store holds a credential for d, per store format --
// the credresolver kind table's own StoreConfig shape for store.Name, not a
// parallel copy of it.
func storeLookupConfig(store Store, d ecosystem.Declaration) (credresolver.Config, error) {
	kind, ok := credresolver.KindBySourceKey(store.Name)
	if !ok || kind.StoreConfig == nil {
		return credresolver.Config{}, fmt.Errorf("registrydiscover: unknown store %q", store.Name)
	}
	return kind.StoreConfig(store.Path, declarationFacts(d)), nil
}
