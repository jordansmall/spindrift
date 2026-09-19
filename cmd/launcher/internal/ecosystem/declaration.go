package ecosystem

// Declaration is one registry declaration extracted from a committed config file.
type Declaration struct {
	Ecosystem       string // stamped by the walker from the matching Table row's Name
	ConfigPath      string // repo-relative path it came from
	Host            string // url.URL.Host (hostname, plus ":port" if present)
	UpstreamBaseURL string // absolute http(s) URL, trailing "/" trimmed
	RegistryName    string // named sub-registry it came from (e.g. cargo's [registries.<name>]), else ""
}

// Note reports a config file that exists but yields no Declaration row.
type Note struct {
	ConfigPath string
	Ecosystem  string
	// Skipped false means the file names no registry at all; true means every
	// registry it named was unusable (non-http(s), userinfo, or an unparseable
	// URL), which an operator must not mistake for "nothing declared".
	Skipped bool
}
