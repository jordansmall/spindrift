// Package gitremote parses git remote URLs into a host and "owner/repo" slug.
package gitremote

import "strings"

// ParseHostSlug extracts the host and "owner/repo" slug from a git remote URL
// in scp-like ssh, ssh:// or https form, less any trailing ".git". It returns
// ("","") for anything it cannot parse, including a path that does not hold
// exactly one "/": Forgejo and Gitea repos never have nested groups.
func ParseHostSlug(remoteURL string) (host, slug string) {
	s := strings.TrimSpace(remoteURL)
	s = strings.TrimSuffix(s, ".git")

	hasScheme := false
	if i := strings.Index(s, "://"); i >= 0 {
		hasScheme = true
		s = s[i+len("://"):]
	}
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[i+1:]
	}

	var path string
	if hasScheme {
		// With a scheme, ":" after the host introduces a port, so only "/"
		// separates host from path.
		slashIdx := strings.Index(s, "/")
		if slashIdx < 0 {
			return "", ""
		}
		host = s[:slashIdx]
		path = s[slashIdx+1:]
		if i := strings.Index(host, ":"); i >= 0 {
			host = host[:i]
		}
	} else {
		// Without a scheme (git@host:owner/repo), whichever of ":" or "/"
		// comes first separates host from path.
		colonIdx := strings.Index(s, ":")
		slashIdx := strings.Index(s, "/")
		var sep int
		switch {
		case colonIdx < 0 && slashIdx < 0:
			return "", ""
		case colonIdx < 0:
			sep = slashIdx
		case slashIdx < 0:
			sep = colonIdx
		case colonIdx < slashIdx:
			sep = colonIdx
		default:
			sep = slashIdx
		}

		host = s[:sep]
		path = s[sep+1:]
	}

	if host == "" || strings.ContainsAny(host, " \t") {
		return "", ""
	}

	path = strings.Trim(path, "/")
	if path == "" || strings.Count(path, "/") != 1 {
		return "", ""
	}

	return host, path
}
