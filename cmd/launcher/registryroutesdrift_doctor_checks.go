package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/gitremote"
	"spindrift.dev/launcher/internal/registrydiscover"
	"spindrift.dev/launcher/internal/registryroutes"
)

// registryRouteDriftRepoDirFn resolves the git checkout root enclosing the
// launcher's own working directory, or "" when none is found. This is
// *candidate* checkout resolution only -- the cwd checkout is not
// automatically the Target repo, so registryRouteDriftCheck pairs this with
// checkoutIsTargetRepo to confirm the candidate's identity before reading
// anything from it. Gating on a real git checkout matters because
// registrydiscover.Extract returns (nil, nil, nil) for a directory with no
// config files, indistinguishable from a checkout that genuinely declares no
// registries -- only a real checkout makes "no drift" meaningful. A seam var
// so a test can point the check at a t.TempDir() fixture instead of this
// process's own working directory.
var registryRouteDriftRepoDirFn = func() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return gitCheckoutRoot(cwd), nil
}

// registryRouteDriftOriginRemoteFn returns root's "origin" remote URL, or ""
// when there is no origin remote or git itself is unavailable. A seam var so
// a test can stub git's absence/output without requiring a real git binary
// on PATH; quickstart/main.go's GitRemoteURL runs the identical command for
// the same reason.
var registryRouteDriftOriginRemoteFn = func(root string) string {
	out, err := exec.Command("git", "-C", root, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// checkoutIsTargetRepo positively identifies whether root is a checkout of
// the Target repo configured by c, by matching root's origin remote against
// c's Code Forge identity. This is the guard that makes the drift row safe
// to run at all: absent it, a cwd checkout that happens to be the Consumer
// flake (a distinct role from the Target repo per CONTEXT.md, even when they
// are the same repo) would have its own drift silently reported as the
// Target repo's -- a false "no drift" (or a false finding) whenever the two
// roles differ. codeForge values other than "git"/"github"/"forgejo" (e.g.
// "local") never match: there is no remote-based Target identity to check
// against.
func checkoutIsTargetRepo(root string, c config) bool {
	remote := registryRouteDriftOriginRemoteFn(root)
	if remote == "" {
		return false
	}
	switch c.codeForge {
	case "git":
		if c.codeForgeRemoteURL == "" {
			return false
		}
		if normalizeGitRemoteURL(remote) == normalizeGitRemoteURL(c.codeForgeRemoteURL) {
			return true
		}
		// Raw compare misses equivalent spellings of the same host+repo
		// (e.g. scp-like "git@host:owner/repo.git" vs "ssh://git@host/owner/repo.git").
		// Fall back to comparing parsed host+slug, but only when both sides
		// actually parse -- a plain path or other form ParseHostSlug can't
		// handle must not spuriously match on two empty results.
		remoteHost, remoteSlug := gitremote.ParseHostSlug(remote)
		wantHost, wantSlug := gitremote.ParseHostSlug(c.codeForgeRemoteURL)
		return remoteHost != "" && remoteSlug != "" &&
			strings.EqualFold(remoteHost, wantHost) && strings.EqualFold(remoteSlug, wantSlug)
	case "github":
		if c.repoSlug == "" {
			return false
		}
		host, slug := gitremote.ParseHostSlug(remote)
		// GH_HOST-configured GitHub Enterprise hosts never match here --
		// skipped, not wrong.
		return host == "github.com" && strings.EqualFold(slug, c.repoSlug)
	case "forgejo":
		if c.repoSlug == "" || c.forgejoBaseURL == "" {
			return false
		}
		base, err := url.Parse(c.forgejoBaseURL)
		if err != nil || base.Host == "" {
			return false
		}
		host, slug := gitremote.ParseHostSlug(remote)
		return strings.EqualFold(host, base.Host) && strings.EqualFold(slug, c.repoSlug)
	default:
		return false
	}
}

// normalizeGitRemoteURL trims whitespace and one trailing ".git"/"/" so
// equivalent codeForge=git remote spellings (with or without the ".git"
// suffix or a trailing slash) compare equal.
func normalizeGitRemoteURL(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return s
}

// gitCheckoutRoot walks up from dir looking for a ".git" entry (os.Stat
// succeeding is enough -- a plain checkout has a ".git" directory, a
// worktree has a ".git" file pointing at the parent checkout's worktree
// metadata, and either marks dir as inside a checkout), returning the
// containing directory as the checkout root. Returns "" if the walk reaches
// the filesystem root without finding one.
func gitCheckoutRoot(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// registryRouteDriftCheck returns a single doctor.Check row reporting
// registry-routes drift (ADR 0045: "doctor re-runs discovery in check mode
// and reports drift -- the repo names host X; no route covers it"): a
// declared host (registrydiscover.Extract's own host enumeration) that no
// route in c.registryProxyRoutesFile covers.
//
// Gated the same way registryRouteChecks (slice 1) is: nil when the routes
// file is unset, unreadable, or unparsable -- deferring to the existing
// registry-proxy-routes row for that failure. Beyond that, the source the
// row reads from -- and the second gate that can also skip it -- depends on
// c.codeForge; see registryRouteDriftCheckForRoutes.
func registryRouteDriftCheck(c config) []doctor.Check {
	if c.registryProxyRoutesFile == "" {
		return nil
	}
	routes, err := loadRegistryRoutes(c.registryProxyRoutesFile)
	if err != nil {
		return nil
	}
	return registryRouteDriftCheckForRoutes(c, routes)
}

// registryRouteDriftCheckForRoutes picks the drift row's source the same
// way deriveHostRootedPathSets (registryroutesresolve.go) does: keyed on
// backendByName(c.codeForge)'s HostMediatedRemote, never a raw
// c.codeForge == "local" string compare that a future host-mediated backend
// would silently miss.
//
// A host-mediated forge reads c.codeForgeAccumulationRepoDir's c.baseBranch
// ref instead of a cwd checkout: the Accumulation repo is bare, so
// checkoutIsTargetRepo's remote-based identity check has no working tree to
// run against, and that ref is the same snapshot the launch itself derives
// host-rooted routes from.
//
// Every other forge reads the cwd checkout, and skips the row unless
// registryRouteDriftRepoDirFn resolves a root checkoutIsTargetRepo can
// positively identify as the Target repo. Outside the dogfood/same-repo
// setup that cwd checkout is the Consumer flake (CONTEXT.md), whose own
// config may say nothing about the Target repo's actual registries, so
// reading it would report a false "no drift" rather than a meaningful
// answer.
//
// Split out from registryRouteDriftCheck so doctorCheckSets
// (bwrap_doctor_checks.go) can hand it the one routes slice it already
// parsed for registryRouteChecks' per-route rows, instead of this package
// parsing the routes file a second time per doctor run.
func registryRouteDriftCheckForRoutes(c config, routes []registryroutes.Route) []doctor.Check {
	row, _ := backendByName(c.codeForge)
	if row.HostMediatedRemote {
		return registryRouteDriftCheckForRef(c.codeForgeAccumulationRepoDir, c.baseBranch, routes)
	}

	repoDir, err := registryRouteDriftRepoDirFn()
	if err != nil || repoDir == "" || !checkoutIsTargetRepo(repoDir, c) {
		return nil
	}
	return []doctor.Check{registryRouteDriftCheckFor(repoDir, routes)}
}

// registryRouteDriftCheckForRef builds the drift row for a host-mediated
// forge's Accumulation repo, or nil when ref can't be resolved -- the same
// nil-row skip registryRouteDriftCheckForRoutes' cwd-checkout path produces
// for a missing checkout. Only registrydiscover.ResolveRef runs eagerly
// here, and it materializes nothing, so a doctor.RunChecksFailFast run that
// never reaches this row's Probe (e.g. an earlier Required check blocking
// first) has no snapshot dir left behind to leak. The Probe itself
// materializes the ref lazily via UncoveredHostsFromGitRef, which cleans up
// its own snapshot dir before returning either way -- at the cost of
// re-running the same rev-parse ResolveRef just ran, which is cheaper than
// holding a snapshot dir open across a Probe that may never run.
func registryRouteDriftCheckForRef(repoDir, ref string, routes []registryroutes.Route) []doctor.Check {
	if err := registrydiscover.ResolveRef(repoDir, ref); err != nil {
		return nil
	}
	check := registryRouteDriftRow(routes, func(covered []string) ([]string, error) {
		return registrydiscover.UncoveredHostsFromGitRef(repoDir, ref, covered)
	})
	return []doctor.Check{check}
}

// registryRouteDriftCheckName is the registry-route-drift row's Name,
// factored into a const so the row's Name field and its SuccessMsg closure
// can't drift apart on a future rename (issue #2853).
const registryRouteDriftCheckName = "registry-route-drift"

// registryRouteDriftCheckFor builds the drift row for an already-resolved
// cwd checkout dir and already-parsed routes. Split out so a test can hand
// it a fixture dir directly, bypassing registryRouteDriftRepoDirFn's real
// checkout resolution.
func registryRouteDriftCheckFor(dir string, routes []registryroutes.Route) doctor.Check {
	return registryRouteDriftRow(routes, func(covered []string) ([]string, error) {
		return registrydiscover.UncoveredHosts(dir, covered)
	})
}

// registryRouteDriftRow builds the drift row shared by both sources --
// registryRouteDriftCheckFor's cwd checkout and registryRouteDriftCheckForRef's
// Accumulation repo ref -- differing only in how the Probe learns the
// uncovered hosts, which probeUncovered supplies.
func registryRouteDriftRow(routes []registryroutes.Route, probeUncovered func(covered []string) ([]string, error)) doctor.Check {
	covered := make([]string, len(routes))
	for i, r := range routes {
		covered[i] = r.MatchHost
	}
	return doctor.Check{
		Name:   registryRouteDriftCheckName,
		Tier:   doctor.Advisory,
		Remedy: "add a route for each listed host to the routes file by hand, or regenerate the whole file with `spindrift registry discover <repo-dir> <routes-file> --force` -- discarding hand edits (ADR 0045)",
		Probe: func() (any, error) {
			hosts, err := probeUncovered(covered)
			if err != nil {
				return nil, fmt.Errorf("%w: %w", err, doctor.ErrDegraded)
			}
			if len(hosts) > 0 {
				return nil, fmt.Errorf("repo names %s; no route covers it", strings.Join(hosts, ", "))
			}
			return "no drift", nil
		},
		SuccessMsg: func(output any) string {
			return fmt.Sprintf("%s (%s)", registryRouteDriftCheckName, output)
		},
	}
}
