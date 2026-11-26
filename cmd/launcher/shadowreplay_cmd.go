package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/shadowreplay"
)

// defaultShadowReplayDir is where the host archives each Dispatch's Pass log.
const defaultShadowReplayDir = ".spindrift/logs"

// cmdShadowReplay replays one Driver's archived Pass logs through its
// Connector beside its native Strategy, and writes each log's comparison to a
// sidecar next to it. A log that cannot be replayed is reported and fails the
// verb at the end, without stopping the rest.
func cmdShadowReplay(connectors *connectorruntime.Runtime, args []string, stdout, stderr io.Writer) int {
	name, connector, dir, err := parseShadowReplayArgs(args)
	if err == nil && connector == "" {
		connector, _ = connectors.Drivers().ShadowModule(name)
	}
	native, hasNative := connectors.Drivers().NativeStrategy(name)
	switch {
	case err != nil:
	case !hasNative:
		err = fmt.Errorf("driver %q has no native Strategy to compare against", name)
	case connector == "":
		err = fmt.Errorf("driver %q has no shadow row; pass --connector <driver.wasm>", name)
	}
	var wasm []byte
	if err == nil {
		wasm, err = os.ReadFile(connector)
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s\n%s\n", err, subcommandUsage("shadow-replay"))
		return 1
	}
	logs, err := filepath.Glob(filepath.Join(dir, "issue-*.log"))
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}
	if len(logs) == 0 {
		// A bare `gh run download <run-id>` nests each artifact under
		// agent-logs-<n>/, so the glob above misses them.
		fmt.Fprintf(stderr, "no Pass logs (issue-*.log) found in %s; if downloaded with `gh run download <run-id>`, pass the nested agent-logs-<n> directory or use `gh run download <run-id> -n agent-logs-<n>`\n", dir)
		return 1
	}
	code := 0
	for _, log := range logs {
		res, err := shadowreplay.Replay(context.Background(), name, native, connectors.Load, wasm, log)
		switch {
		case err != nil:
			fmt.Fprintf(stderr, "%s: %s\n", log, err)
			code = 1
		case res.Skipped != "":
			fmt.Fprintf(stdout, "%s: skipped: %s\n", log, res.Skipped)
		default:
			fmt.Fprintf(stdout, "%s: replayed %d Passes -> %s\n", log, res.Passes, res.Sidecar)
		}
	}
	return code
}

func parseShadowReplayArgs(args []string) (name, connector, dir string, err error) {
	dir = defaultShadowReplayDir
	var dirs []string
	for i := 0; i < len(args); i++ {
		flag, value, hasValue := strings.Cut(args[i], "=")
		switch flag {
		case "--driver", "--connector":
			if !hasValue {
				if i+1 >= len(args) {
					return "", "", "", fmt.Errorf("flag %s requires a value", flag)
				}
				i++
				value = args[i]
			}
			if flag == "--driver" {
				name = value
			} else {
				connector = value
			}
		default:
			if strings.HasPrefix(args[i], "-") {
				return "", "", "", fmt.Errorf("unrecognized argument: %s", args[i])
			}
			dirs = append(dirs, args[i])
		}
	}
	switch {
	case name == "":
		return "", "", "", errors.New("--driver is required")
	case len(dirs) > 1:
		return "", "", "", errors.New("shadow-replay takes at most one log directory")
	case len(dirs) == 1:
		dir = dirs[0]
	}
	return name, connector, dir, nil
}
