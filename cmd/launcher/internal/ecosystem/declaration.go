package ecosystem

// Declaration is one registry declaration extracted from a committed config
// file.
type Declaration struct {
	Ecosystem       string // stamped by the walker from the matching Table row's Name
	ConfigPath      string // repo-relative path it came from
	Host            string // url.URL.Host (hostname, plus ":port" if present)
	UpstreamBaseURL string // absolute http(s) URL, trailing "/" trimmed
	RegistryName    string // named sub-registry it came from (e.g. cargo's [registries.<name>]), else ""
}

// Note is a per-config-file observation for the report: the file exists but
// yields no Declaration row -- worth surfacing to the operator all the same.
// Skipped distinguishes *why*: false means the file
// genuinely names no registry at all; true means it named one or more
// registries but every one was unusable (non-http(s), userinfo, or an
// unparseable URL) -- a materially different situation an operator must not
// mistake for "nothing declared".
type Note struct {
	ConfigPath string
	Ecosystem  string
	Skipped    bool
}
