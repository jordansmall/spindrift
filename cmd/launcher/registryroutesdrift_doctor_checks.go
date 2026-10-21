package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrydiscover"
	"spindrift.dev/launcher/internal/registryroutes"
)

// registryRouteDriftRepoDirFn resolves the git checkout root enclosing the
// launcher's working directory, or "" when none is found. Callers must confirm
// the candidate with checkoutIsTargetRepo; requiring a real checkout matters
// because registrydiscover.Extract returns (nil, nil, nil) both for a
// config-less directory and for a checkout that declares no registries.
var registryRouteDriftRepoDirFn = func() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return gitCheckoutRoot(cwd), nil
}

// registryRouteDriftOriginRemoteFn returns root's "origin" remote URL, or ""
// when there is no origin remote or git is unavailable. A seam var so a test
// can stub git without a real git binary on PATH.
var registryRouteDriftOriginRemoteFn = func(root string) string {
	out, err := exec.Command("git", "-C", root, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// checkoutIsTargetRepo reports whether root is a checkout of the Target repo
// configured by c, by matching root's origin remote against c's Code Forge
// identity. Without this guard the check would report a cwd checkout that is
// really the Consumer flake as the Target repo's drift. A Code Forge with no
// remote identity never matches.
func checkoutIsTargetRepo(root string, c config) bool {
	remote := registryRouteDriftOriginRemoteFn(root)
	if remote == "" {
		return false
	}
	row, ok := backendkit.ByName(c.codeForge)
	return ok && row.RemoteMatch != nil && row.RemoteMatch(backendShared(c), c.knobLookup, remote)
}

// gitCheckoutRoot walks up from dir to the directory holding a ".git" entry,
// or "" if the walk reaches the filesystem root. A successful os.Stat is
// enough: a plain checkout has a ".git" directory and a worktree has a ".git"
// file, and either one marks dir as inside a checkout.
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

// registryRouteDriftCheckForRoutes keys the row's source on
// backendkit.ByName(c.codeForge).HostMediatedRemote, not a codeForge == "local"
// compare a future host-mediated backend would break. A host-mediated forge
// reads the bare Accumulation repo at c.baseBranch, the same snapshot the
// launch itself derives host-rooted routes from.
func registryRouteDriftCheckForRoutes(c config, routes []registryroutes.Route, rows []ecosystem.Row) []doctor.Check {
	row, _ := backendkit.ByName(c.codeForge)
	if row.HostMediatedRemote {
		return registryRouteDriftCheckForRef(c.hostRepoDir(), c.baseBranch, routes, rows)
	}

	repoDir, err := registryRouteDriftRepoDirFn()
	if err != nil || repoDir == "" || !checkoutIsTargetRepo(repoDir, c) {
		return nil
	}
	return []doctor.Check{registryRouteDriftCheckFor(repoDir, routes, rows)}
}

// registryRouteDriftCheckForRef builds the drift row for a host-mediated
// forge's Accumulation repo, or nil when ref cannot be resolved. Only
// ResolveRef runs eagerly, and it materializes nothing, so a fail-fast run
// that never reaches the Probe leaks no snapshot dir. The Probe materializes
// the ref itself and cleans up, at the cost of a repeated rev-parse.
func registryRouteDriftCheckForRef(repoDir, ref string, routes []registryroutes.Route, rows []ecosystem.Row) []doctor.Check {
	if err := registrydiscover.ResolveRef(repoDir, ref); err != nil {
		return nil
	}
	check := registryRouteDriftRow(routes, func(covered []string) ([]string, error) {
		return registrydiscover.UncoveredHostsFromGitRefRows(repoDir, ref, covered, rows)
	})
	return []doctor.Check{check}
}

// registryRouteDriftCheckName keeps the row's Name and its SuccessMsg closure
// from drifting apart on a rename (issue #2853).
const registryRouteDriftCheckName = "registry-route-drift"

// registryRouteDriftCheckFor builds the drift row for an already-resolved cwd
// checkout, so a test can hand it a fixture dir directly.
func registryRouteDriftCheckFor(dir string, routes []registryroutes.Route, rows []ecosystem.Row) doctor.Check {
	return registryRouteDriftRow(routes, func(covered []string) ([]string, error) {
		return registrydiscover.UncoveredHostsRows(dir, covered, rows)
	})
}

// registryRouteDriftRow builds the drift row shared by both sources, which
// differ only in the probeUncovered they supply.
func registryRouteDriftRow(routes []registryroutes.Route, probeUncovered func(covered []string) ([]string, error)) doctor.Check {
	covered := make([]string, len(routes))
	for i, r := range routes {
		covered[i] = r.MatchHost
	}
	return doctor.Check{
		Name:   registryRouteDriftCheckName,
		Tier:   doctor.Advisory,
		Remedy: "add a route for each listed host to the routes file by hand, or regenerate the whole file by running `" + subcommandSynopsis("registry") + "` with --force (required to overwrite the existing file) -- discarding hand edits (ADR 0045)",
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
