// Package gitremote parses git remote URLs into a host and "owner/repo"
// slug. It exists as its own leaf package (rather than living in the
// quickstart wizard that originated it) so any launcher package that needs
// to identify which host+repo a checkout's origin remote points at --
// quickstart's Forgejo/Codeberg detection, doctor's registry-route-drift
// Target-repo identity check -- shares one parser instead of hand-rolling
// its own.
package gitremote

import "strings"

// ParseHostSlug extracts the host and "owner/repo" slug from a git remote
// URL in any common form -- scp-like ssh (git@host:owner/repo.git), ssh://
// (ssh://git@host/owner/repo.git), or https (https://host/owner/repo.git)
// -- stripping a trailing ".git". Forgejo/Gitea repos are always a single
// owner/repo pair (no nested groups), so a path that is not exactly one "/"
// apart yields ("",""). Returns ("","") for any input it cannot parse into
// host + owner/repo.
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
		// For a scheme-based remote, ":" after the host introduces a port,
		// not the host/path separator — only "/" separates host from path.
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
		// scp-like remote (e.g. git@host:owner/repo): ":" or the first "/",
		// whichever comes first, separates host from path.
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
