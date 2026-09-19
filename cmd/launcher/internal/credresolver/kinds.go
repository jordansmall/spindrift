package credresolver

import "slices"

// StoreFacts is a dependency-free stand-in for an ecosystem.Declaration:
// credresolver imports stdlib only and must never grow an import of
// ecosystem, registrydiscover, or registryvocab. Host and HostKey hold
// different values: npmrc's store keys on the raw host, gradle-properties'
// on registryvocab.HostKey(host).
type StoreFacts struct {
	Host            string
	HostKey         string
	UpstreamBaseURL string
	RegistryName    string
}

// Kind is one row of the seven-source table. kindTable's row order is the
// canonical source-key order, which callers read back through SourceKeys().
type Kind struct {
	// SourceKey is the operator-facing TOML key naming this source (ADR 0045).
	SourceKey string
	// CompanionKey is the TOML key alongside SourceKey supplying a second
	// required value, "" when this kind takes no companion. A companion key
	// is never a source key in its own right.
	CompanionKey string
	// FileFormat is "" for env and exec, neither of which is file-backed.
	FileFormat string
	// ArgvValue is true only for exec, whose TOML value is an argv array
	// rather than a string, so parseCredential must pull it out of the
	// generic string/empty check.
	ArgvValue bool

	// ValueField selects the Config field this kind's primary value lands
	// in. nil only for exec, which lands in Config.ExecArgv instead.
	ValueField func(*Config) *string
	// CompanionField is nil when CompanionKey is "".
	CompanionField func(*Config) *string

	// StorePath is this kind's store file as $HOME-relative path segments,
	// nil for the three kinds that are not stores (env, exec, file/raw).
	StorePath []string
	// StoreConfig builds the Config a discovery-side store lookup produces,
	// given the store's resolved path and the route's facts. nil when the
	// kind is not a store.
	StoreConfig func(path string, f StoreFacts) Config
	// StoreApplicable reports whether this store kind applies given f; nil
	// means always applicable. Only cargo-credentials sets it, because a
	// cargo store lookup needs the ecosystem to name a registry first.
	StoreApplicable func(f StoreFacts) bool

	// newFileResolver is non-nil only for the five file-backed kinds. New
	// walks the table matching FileFormat rather than switching on it.
	newFileResolver func(Config) Resolver

	// storeSearchOrder is this store kind's position in the documented
	// search order, which differs from kindTable's row order. Zero and
	// unused on the three non-store kinds.
	storeSearchOrder int
}

// kindTable is the seven credential sources in canonical source-key order.
// registryroutes takes that order from here for its "names more than one
// source" error text and its retired-key renderer, so reordering a row here
// is operator-visible.
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
// still shares its StorePath backing array with kindTable, so a caller
// writing StorePath[0] would rewrite the table for every later call.
func (k Kind) clone() Kind {
	if k.StorePath != nil {
		k.StorePath = append([]string(nil), k.StorePath...)
	}
	return k
}

// Kinds returns the seven-entry table in kindTable's order, each row a fresh
// copy so a caller mutating the result cannot corrupt the table.
func Kinds() []Kind {
	out := make([]Kind, len(kindTable))
	for i, k := range kindTable {
		out[i] = k.clone()
	}
	return out
}

// KindBySourceKey looks up a kind by its operator-facing TOML key, reporting
// false when key names none of the seven.
func KindBySourceKey(key string) (Kind, bool) {
	for _, k := range kindTable {
		if k.SourceKey == key {
			return k.clone(), true
		}
	}
	return Kind{}, false
}

// SourceKeys returns the seven operator-facing TOML keys in kindTable's
// order.
func SourceKeys() []string {
	out := make([]string, len(kindTable))
	for i, k := range kindTable {
		out[i] = k.SourceKey
	}
	return out
}

// IsCompanionKey reports whether key is a companion key (registry-name, key)
// rather than a source key. A companion key is never valid as a route's
// top-level credential source.
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

// StoreKinds returns the four discoverable stores in the documented search
// order, which differs from kindTable's row order. It is the single source
// of truth for both that order and each store's path; registrydiscover walks
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
