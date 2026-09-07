package credresolver

import "slices"

// StoreFacts is the dependency-free stand-in for an ecosystem.Declaration:
// credresolver imports stdlib only (it must never grow an import of
// ecosystem, registrydiscover, or registryvocab), so a store kind's
// StoreConfig takes this instead of the real declaration type. Host and
// HostKey are deliberately distinct fields, not one field two callers
// reinterpret: npmrc's store keys on the raw host, gradle-properties' keys
// on registryvocab.HostKey(host), and those are not the same value.
type StoreFacts struct {
	Host            string
	HostKey         string
	UpstreamBaseURL string
	RegistryName    string
}

// Kind is one row of the seven-source table: the operator-facing TOML
// spelling plus every column New and a discovery-side store lookup dispatch
// on. kindTable's row order is the canonical source-key order, which callers
// read back through SourceKeys().
type Kind struct {
	// SourceKey is the operator-facing TOML key naming this source (ADR
	// 0045), e.g. "env", "cargo-credentials".
	SourceKey string
	// CompanionKey is the TOML key alongside SourceKey supplying a second
	// required value (RegistryName, PropertyKey) -- "" when this kind
	// takes no companion. registry-name and key are companions, never
	// sources, in their own right.
	CompanionKey string
	// FileFormat is the value New's Config.FileFormat takes for this kind
	// -- "" for env and exec, neither of which is file-backed.
	FileFormat string
	// ArgvValue is true only for exec, whose TOML value is an argv array
	// rather than a string -- the one kind parseCredential must pull out
	// of the generic string/empty check.
	ArgvValue bool

	// ValueField selects the Config field this kind's primary value lands
	// in. nil only for exec, which lands in Config.ExecArgv instead (see
	// ArgvValue).
	ValueField func(*Config) *string
	// CompanionField selects the Config field this kind's companion value
	// lands in -- nil when CompanionKey is "".
	CompanionField func(*Config) *string

	// StorePath is this kind's discoverable store file as $HOME-relative
	// path segments, nil when the kind is not a store. env and exec are
	// never searched (env is only the unmatched-host placeholder); file/raw
	// is not a store either -- only four of the seven kinds are.
	StorePath []string
	// StoreConfig builds the Config a discovery-side store lookup produces
	// for this kind, given the store's resolved path and the route's
	// facts. nil when the kind is not a store.
	StoreConfig func(path string, f StoreFacts) Config
	// StoreApplicable reports whether this store kind applies given f; nil
	// means always applicable. Only cargo-credentials' is non-nil: a cargo
	// store lookup only makes sense once the ecosystem names a registry.
	StoreApplicable func(f StoreFacts) bool

	// newFileResolver builds New's Resolver adapter for this kind from a
	// Config. Non-nil only for the five file-backed kinds (file/raw,
	// netrc, cargo-credentials, npmrc, gradle-properties) -- New walks the
	// table by matching FileFormat rather than switching on it directly.
	newFileResolver func(Config) Resolver

	// storeSearchOrder is this store kind's position in the documented
	// search order (netrc, npmrc, cargo-credentials, gradle-properties) --
	// zero and unused on the three non-store kinds. Deliberately differs
	// from kindTable's own row order, which places cargo-credentials
	// before npmrc.
	storeSearchOrder int
}

// kindTable is the seven credential sources in canonical source-key order.
// registryroutes takes that order from here (its credentialSourceKeys is
// credresolver.SourceKeys()), and it is load-bearing twice over there: the
// "names more than one source" error text and the retired-key renderer's key
// order both key off it, so reordering a row here is operator-visible.
var kindTable = []Kind{
	{
		SourceKey:  "env",
		ValueField: func(c *Config) *string { return &c.FromEnv },
	},
	{
		SourceKey:  "file",
		FileFormat: "raw",
		ValueField: func(c *Config) *string { return &c.FromFile },
		newFileResolver: func(c Config) Resolver {
			return peekOnly{rawFileResolver{path: c.FromFile}}
		},
	},
	{
		SourceKey:        "netrc",
		FileFormat:       "netrc",
		ValueField:       func(c *Config) *string { return &c.FromFile },
		StorePath:        []string{".netrc"},
		storeSearchOrder: 0,
		StoreConfig: func(path string, f StoreFacts) Config {
			return Config{FromFile: path, FileFormat: "netrc", UpstreamURL: f.UpstreamBaseURL}
		},
		newFileResolver: func(c Config) Resolver {
			return peekOnly{netrcFileResolver{path: c.FromFile, upstreamURL: c.UpstreamURL}}
		},
	},
	{
		SourceKey:        "cargo-credentials",
		CompanionKey:     "registry-name",
		FileFormat:       "cargo-credentials",
		ValueField:       func(c *Config) *string { return &c.FromFile },
		CompanionField:   func(c *Config) *string { return &c.RegistryName },
		StorePath:        []string{".cargo", "credentials.toml"},
		storeSearchOrder: 2,
		StoreConfig: func(path string, f StoreFacts) Config {
			return Config{FromFile: path, FileFormat: "cargo-credentials", RegistryName: f.RegistryName}
		},
		StoreApplicable: func(f StoreFacts) bool { return f.RegistryName != "" },
		newFileResolver: func(c Config) Resolver {
			return peekOnly{cargoFileResolver{path: c.FromFile, registryName: c.RegistryName}}
		},
	},
	{
		SourceKey: "exec",
		ArgvValue: true,
	},
	{
		SourceKey:        "npmrc",
		FileFormat:       "npmrc",
		ValueField:       func(c *Config) *string { return &c.FromFile },
		StorePath:        []string{".npmrc"},
		storeSearchOrder: 1,
		StoreConfig: func(path string, f StoreFacts) Config {
			return Config{FromFile: path, FileFormat: "npmrc", MatchHost: f.Host}
		},
		newFileResolver: func(c Config) Resolver {
			return peekOnly{npmrcFileResolver{path: c.FromFile, matchHost: c.MatchHost}}
		},
	},
	{
		SourceKey:        "gradle-properties",
		CompanionKey:     "key",
		FileFormat:       "gradle-properties",
		ValueField:       func(c *Config) *string { return &c.FromFile },
		CompanionField:   func(c *Config) *string { return &c.PropertyKey },
		StorePath:        []string{".gradle", "gradle.properties"},
		storeSearchOrder: 3,
		StoreConfig: func(path string, f StoreFacts) Config {
			return Config{FromFile: path, FileFormat: "gradle-properties", PropertyKey: f.HostKey}
		},
		newFileResolver: func(c Config) Resolver {
			return peekOnly{gradlePropertiesFileResolver{path: c.FromFile, propertyKey: c.PropertyKey}}
		},
	},
}

// clone is the copy every accessor below hands out. A Kind copied by value
// still shares its StorePath backing array with the package's own table, so
// a caller writing StorePath[0] would rewrite the table for every later
// call.
func (k Kind) clone() Kind {
	if k.StorePath != nil {
		k.StorePath = append([]string(nil), k.StorePath...)
	}
	return k
}

// Kinds returns the seven-entry table in kindTable's order. Each
// row is a copy per call, StorePath included, so a caller mutating its
// result can't corrupt the package's own table for a later call.
func Kinds() []Kind {
	out := make([]Kind, len(kindTable))
	for i, k := range kindTable {
		out[i] = k.clone()
	}
	return out
}

// KindBySourceKey looks up a kind by its operator-facing TOML key (e.g.
// "cargo-credentials"), reporting false when key names none of the seven.
func KindBySourceKey(key string) (Kind, bool) {
	for _, k := range kindTable {
		if k.SourceKey == key {
			return k.clone(), true
		}
	}
	return Kind{}, false
}

// SourceKeys returns the seven operator-facing TOML keys, in kindTable's
// order -- registryroutes' credentialSourceKeys is this call.
func SourceKeys() []string {
	out := make([]string, len(kindTable))
	for i, k := range kindTable {
		out[i] = k.SourceKey
	}
	return out
}

// IsCompanionKey reports whether key is one of the companion keys
// (registry-name, key) rather than a source key -- a companion key is
// never valid as a route's top-level credential source.
func IsCompanionKey(key string) bool {
	if key == "" {
		return false
	}
	for _, k := range kindTable {
		if k.CompanionKey == key {
			return true
		}
	}
	return false
}

// StoreKinds returns the four discoverable stores (netrc, npmrc,
// cargo-credentials, gradle-properties) in the documented search order --
// distinct from kindTable's own row order (see Kind.storeSearchOrder).
// env, exec, and file/raw are never stores, so they're excluded. This is
// the single source of truth for both the search order and each store's
// path; cmd/launcher/registrydiscover.go derives its store list by walking
// this slice rather than keeping its own copy.
func StoreKinds() []Kind {
	var out []Kind
	for _, k := range kindTable {
		if k.StorePath != nil {
			out = append(out, k.clone())
		}
	}
	slices.SortFunc(out, func(a, b Kind) int { return a.storeSearchOrder - b.storeSearchOrder })
	return out
}
