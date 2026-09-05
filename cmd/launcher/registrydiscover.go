package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/registrydiscover"
)

// runRegistryDiscover is the testable core of `spindrift registry discover`:
// it runs the discovery engine against repoDir with the given stores,
// lookup, and probe, writes the routes file to outPath (refusing an
// existing file unless force), prints a human report to stdout, and returns
// a process exit code. Errors go to stderr instead, matching every sibling
// verb (doctor, reconcile): a caller piping stdout to a report file must
// still see failures. The report is only printed once the file is actually
// written -- a discovery or write failure gets just its error, not a report
// describing routes that were never persisted. Zero discovered routes is
// also treated as a failure rather than writing a header-only file
// registryroutes.Parse would reject: it's indistinguishable from a mistyped
// repoDir, so it gets the report (explaining what was and wasn't found) plus
// a clear stderr message naming repoDir, and a non-zero exit.
func runRegistryDiscover(stdout, stderr io.Writer, repoDir, outPath string, force bool, stores []registrydiscover.Store, lookup registrydiscover.Lookup, probe registrydiscover.Probe) int {
	routes, report, err := registrydiscover.Discover(repoDir, stores, lookup, probe)
	if err != nil {
		fmt.Fprintf(stderr, "registry discover: %s\n", err)
		return 1
	}

	if len(routes) == 0 {
		printRegistryDiscoverReport(stdout, report)
		fmt.Fprintf(stderr, "registry discover: no registry declarations found under %s; nothing to write\n", repoDir)
		return 1
	}

	if err := registrydiscover.WriteFile(outPath, routes, force); err != nil {
		fmt.Fprintf(stderr, "registry discover: %s\n", err)
		return 1
	}

	// The upstream printed here is the URL discovery found in the repo's
	// config -- the useful thing to show an operator -- not a key of the
	// file just written: a route matches a host and derives the paths it
	// serves, naming an upstream-origin only when scheme or port differs
	// from that derivation (ADR 0047, issue #3261).
	for _, r := range routes {
		fmt.Fprintf(stdout, "route %s -> %s (auth %s, credential %s)\n", r.MatchHost, r.UpstreamBaseURL, r.AuthScheme, r.CredentialSource)
	}
	printRegistryDiscoverReport(stdout, report)
	printRegistryDiscoverEnvHint(stdout, routes)

	fmt.Fprintf(stdout, "wrote routes file %s\n", outPath)
	return 0
}

// printRegistryDiscoverReport renders Discover's Report: which hosts
// matched a store, which didn't (naming every store searched, in order),
// and which config files declared no registry at all.
func printRegistryDiscoverReport(w io.Writer, report registrydiscover.Report) {
	if len(report.Matched) > 0 {
		fmt.Fprintln(w, "matched:")
		for _, m := range report.Matched {
			fmt.Fprintf(w, "  %s found in %s (%s)\n", m.Host, m.StoreName, m.StorePath)
		}
	}

	if len(report.Unmatched) > 0 {
		fmt.Fprintln(w, "unmatched:")
		for _, u := range report.Unmatched {
			fmt.Fprintf(w, "  %s: searched %s\n", u.Host, strings.Join(u.StoresSearched, ", "))
		}
	}

	var empty, skipped []registrydiscover.Note
	for _, n := range report.NoRegistry {
		if n.Skipped {
			skipped = append(skipped, n)
		} else {
			empty = append(empty, n)
		}
	}

	if len(empty) > 0 {
		fmt.Fprintln(w, "config present, no registry declared:")
		for _, n := range empty {
			fmt.Fprintf(w, "  %s (%s)\n", n.ConfigPath, n.Ecosystem)
		}
	}

	if len(skipped) > 0 {
		fmt.Fprintln(w, "config declares only non-http/unusable registry URLs:")
		for _, n := range skipped {
			fmt.Fprintf(w, "  %s (%s)\n", n.ConfigPath, n.Ecosystem)
		}
	}
}

// printRegistryDiscoverEnvHint warns the operator when any written route
// falls back to the "env" placeholder credential source (Discover's answer
// for a host no store matched) -- that route does nothing until the named
// environment variable is set.
func printRegistryDiscoverEnvHint(w io.Writer, routes []registrydiscover.Route) {
	var placeholders []string
	for _, r := range routes {
		if r.CredentialSource == "env" {
			placeholders = append(placeholders, r.CredentialValue)
		}
	}
	if len(placeholders) > 0 {
		fmt.Fprintf(w, "set these environment variables before relying on the affected routes: %s\n", strings.Join(placeholders, ", "))
	}
}

// cmdRegistryDiscover parses `registry discover`'s own args and fills the
// production deps: the well-known operator store paths under $HOME searched
// in the documented order (netrc, npmrc, cargo-credentials,
// gradle-properties), the real credresolver-backed StoreLookup, and the
// HTTP auth-scheme probe. stdout/stderr are threaded explicitly (mirroring
// runRegistryDiscover's split) so a test can assert on each independently;
// production wiring hands this os.Stdout/os.Stderr (see the verbHandlers
// "registry" entry, main.go).
func cmdRegistryDiscover(args []string, stdout, stderr io.Writer) int {
	force := false
	var positional []string
	for _, a := range args {
		if a == "--force" {
			force = true
			continue
		}
		positional = append(positional, a)
	}
	if len(positional) != 2 {
		fmt.Fprintln(stderr, "usage: spindrift registry discover <repo-dir> <routes-file> [--force]")
		return 1
	}

	stores, err := defaultRegistryDiscoverStores()
	if err != nil {
		fmt.Fprintf(stderr, "registry discover: %s\n", err)
		return 1
	}

	probe := func(u string) string {
		return registrydiscover.HTTPProbe(registrydiscover.DefaultProbeClient(), u)
	}

	return runRegistryDiscover(stdout, stderr, positional[0], positional[1], force, stores, registrydiscover.StoreLookup, probe)
}

// defaultRegistryDiscoverStores names the operator credential stores
// discovery searches, in the documented order. A missing file is fine --
// StoreLookup answers not-found for it and the store is still named in the
// report as searched. os.UserHomeDir failing (no $HOME) is not: proceeding
// with zero stores would make every unmatched-host report line name no
// stores searched at all, so that's surfaced as an error instead of
// silently discarded.
func defaultRegistryDiscoverStores() ([]registrydiscover.Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return []registrydiscover.Store{
		{Name: "netrc", Path: filepath.Join(home, ".netrc")},
		{Name: "npmrc", Path: filepath.Join(home, ".npmrc")},
		{Name: "cargo-credentials", Path: filepath.Join(home, ".cargo", "credentials.toml")},
		{Name: "gradle-properties", Path: filepath.Join(home, ".gradle", "gradle.properties")},
	}, nil
}
