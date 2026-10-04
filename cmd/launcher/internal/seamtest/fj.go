package seamtest

import (
	"fmt"
	"io"
	"os"
)

// FjConfig is the fj fake's JSON config.
type FjConfig struct {
	// Record is the file every invocation's argv is appended to.
	Record string `json:"record"`
	// StdinRecord is the file every invocation's stdin is appended to, so a
	// test can assert a secret reached fj on stdin and never on argv.
	StdinRecord string `json:"stdin_record"`
	// Exit is the status every invocation returns.
	Exit int `json:"exit"`
}

func fjMain(args []string) int {
	return fjFake(args, os.Stdin, os.Stderr)
}

func fjFake(args []string, stdin io.Reader, stderr io.Writer) int {
	var cfg FjConfig
	if err := loadConfig("fj", &cfg); err != nil {
		fmt.Fprintf(stderr, "fj fake: %v\n", err)
		return fakeConfigExit
	}
	if _, err := appendRecord(cfg.Record, args); err != nil {
		fmt.Fprintf(stderr, "fj fake: record: %v\n", err)
		return fakeConfigExit
	}
	if cfg.StdinRecord != "" {
		in, err := io.ReadAll(stdin)
		if err == nil {
			err = appendLocked(cfg.StdinRecord, in, nil)
		}
		if err != nil {
			fmt.Fprintf(stderr, "fj fake: stdin: %v\n", err)
			return fakeConfigExit
		}
	}
	return cfg.Exit
}
