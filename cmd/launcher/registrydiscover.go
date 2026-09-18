package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrydiscover"
)

// runRegistryDiscover is the testable core of `spindrift registry discover`.
// Errors go to stderr, not stdout, so a caller piping stdout to a report file
// still sees failures. The report prints only after the file is written, so a
// failed run never describes routes that were not persisted. Zero routes is a
// failure: a header-only file would fail registryroutes.Parse.
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

	for _, r := range routes {
		fmt.Fprintf(stdout, "route %s (auth %s, credential %s)\n", r.MatchHost, r.AuthScheme, r.CredentialSource)
	}
	printRegistryDiscoverReport(stdout, report)
	printRegistryDiscoverEnvHint(stdout, routes)

	fmt.Fprintf(stdout, "wrote routes file %s\n", outPath)
	return 0
}

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

	var empty, skipped []ecosystem.Note
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

// printRegistryDiscoverEnvHint warns that a route with the "env" placeholder
// credential source does nothing until the named variable is set.
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

// cmdRegistryDiscover is the `registry discover` subcommand: parse its args and
// hand runRegistryDiscover the production stores, StoreLookup, and HTTP probe.
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

// defaultRegistryDiscoverStores names the operator credential stores discovery
// searches, in the documented order. A missing file is fine: StoreLookup
// answers not-found and the store is still reported as searched. A failing
// os.UserHomeDir is not, since zero stores would make every unmatched-host
// line name no stores searched at all.
func defaultRegistryDiscoverStores() ([]registrydiscover.Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	kinds := credresolver.StoreKinds()
	stores := make([]registrydiscover.Store, 0, len(kinds))
	for _, kind := range kinds {
		segments := append([]string{home}, kind.StorePath...)
		stores = append(stores, registrydiscover.Store{Name: kind.SourceKey, Path: filepath.Join(segments...)})
	}
	return stores, nil
}
