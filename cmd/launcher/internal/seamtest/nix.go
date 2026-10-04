package seamtest

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// NixConfig is the nix fake's JSON config. It models only the
// `nix develop .#<attr> ...` probe the Box's toolchain decision runs.
type NixConfig struct {
	// Record is the file every invocation's argv is appended to.
	Record string `json:"record"`
	// DevShells are the flake attrs whose `develop` succeeds; any other attr
	// exits 1, as a flake without that devShell does.
	DevShells []string `json:"devShells"`
}

func nixMain(args []string) int {
	return nixFake(args, os.Stderr)
}

func nixFake(args []string, stderr io.Writer) int {
	var cfg NixConfig
	if err := loadConfig("nix", &cfg); err != nil {
		fmt.Fprintf(stderr, "nix fake: %v\n", err)
		return fakeConfigExit
	}
	if _, err := appendRecord(cfg.Record, args); err != nil {
		fmt.Fprintf(stderr, "nix fake: record: %v\n", err)
		return fakeConfigExit
	}
	if len(args) >= 2 && args[0] == "develop" && strings.HasPrefix(args[1], ".#") {
		for _, name := range cfg.DevShells {
			if args[1] == ".#"+name {
				return 0
			}
		}
	}
	fmt.Fprintf(stderr, "nix fake: no such devShell: %v\n", args)
	return 1
}
