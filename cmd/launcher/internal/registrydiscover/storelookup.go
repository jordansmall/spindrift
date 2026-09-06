package registrydiscover

import (
	"fmt"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registryvocab"
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
// that answers whether store holds a credential for d, per store format.
func storeLookupConfig(store Store, d ecosystem.Declaration) (credresolver.Config, error) {
	switch store.Name {
	case "netrc":
		return credresolver.Config{FromFile: store.Path, FileFormat: "netrc", UpstreamURL: d.UpstreamBaseURL}, nil
	case "npmrc":
		return credresolver.Config{FromFile: store.Path, FileFormat: "npmrc", MatchHost: d.Host}, nil
	case "cargo-credentials":
		return credresolver.Config{FromFile: store.Path, FileFormat: "cargo-credentials", RegistryName: d.RegistryName}, nil
	case "gradle-properties":
		return credresolver.Config{FromFile: store.Path, FileFormat: "gradle-properties", PropertyKey: registryvocab.HostKey(d.Host)}, nil
	default:
		return credresolver.Config{}, fmt.Errorf("registrydiscover: unknown store %q", store.Name)
	}
}
