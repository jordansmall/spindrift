package seamtest

import (
	"fmt"
	"io"
	"os"
)

// GhConfig is the gh fake's JSON config.
type GhConfig struct {
	// Record is the file every invocation's argv is appended to.
	Record string `json:"record"`
	// Replies are tried in order; the first whose Args match answers. An
	// invocation no reply matches succeeds silently, like the shell fake's
	// no-op subcommands (`issue edit`, `pr create`, ...).
	Replies []GhReply `json:"replies"`
}

// GhReply scripts the answer to the invocations it matches.
type GhReply struct {
	// Args match when each element appears in the invocation's argv, in
	// order but not necessarily adjacent, so a reply can name
	// {"issue", "view", "7", "number,title,body,state,labels"} and ignore
	// the --repo flag between them.
	Args   []string `json:"args"`
	Stdout string   `json:"stdout"`
	Exit   int      `json:"exit"`
}

func ghMain(args []string) int {
	return ghFake(args, os.Stdout, os.Stderr)
}

func ghFake(args []string, stdout, stderr io.Writer) int {
	var cfg GhConfig
	if err := loadConfig("gh", &cfg); err != nil {
		fmt.Fprintf(stderr, "gh fake: %v\n", err)
		return fakeConfigExit
	}
	if _, err := appendRecord(cfg.Record, args); err != nil {
		fmt.Fprintf(stderr, "gh fake: record: %v\n", err)
		return fakeConfigExit
	}
	for _, r := range cfg.Replies {
		if containsInOrder(args, r.Args) {
			io.WriteString(stdout, r.Stdout)
			return r.Exit
		}
	}
	return 0
}

func containsInOrder(args, want []string) bool {
	i := 0
	for _, a := range args {
		if i < len(want) && a == want[i] {
			i++
		}
	}
	return i == len(want)
}
