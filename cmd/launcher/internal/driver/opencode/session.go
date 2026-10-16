package opencode

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"

	_ "modernc.org/sqlite"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// sessionStore is the per-issue opencode database, relative to home. The Box
// points OPENCODE_DB at it so it is cached apart from other state.
const sessionStore = ".local/share/opencode/spindrift-session/opencode.db"

// sessionID is the only shape of id rendered into argv. The store is
// agent-writable and driver-exec splits the flags on whitespace, so an
// unchecked id could smuggle extra flags.
var sessionID = regexp.MustCompile(`^ses_[A-Za-z0-9]+$`)

// SessionFlags renders the opencode CLI's session flags. opencode generates
// its own session ids, so no id can be pinned at creation: "initial" pins a
// title derived from repoSlug and issue instead, and "resume" looks up the
// newest root session carrying that title in the store under home. A missing
// store, a failed query, or no match renders nothing and the caller falls back
// to the cold-context fix flow. Never --continue: it names the last session
// globally, not this issue's.
func SessionFlags(mode, repoSlug, issue, home string) string {
	title := "spindrift-" + driverkit.SessionKey(repoSlug, issue)
	switch mode {
	case "initial":
		return "--title " + title
	case "resume":
		if id := lookupSession(filepath.Join(home, sessionStore), title); id != "" {
			return "--session " + id
		}
	}
	return ""
}

// lookupSession returns the newest live root session titled title, or "" on
// any failure or a malformed id; failures other than "no row" warn on stderr.
// The stat guard and mode=rw keep sqlite from creating the file.
func lookupSession(path, title string) string {
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: opening opencode session store %s failed: %v\n", path, err)
		return ""
	}
	defer db.Close()
	var id string
	err = db.QueryRow(`SELECT id FROM session WHERE title = ? AND parent_id IS NULL AND time_archived IS NULL ORDER BY time_created DESC LIMIT 1`, title).Scan(&id)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "WARNING: querying opencode session store %s failed: %v\n", path, err)
		}
		return ""
	}
	if !sessionID.MatchString(id) {
		fmt.Fprintf(os.Stderr, "WARNING: opencode session store %s holds a malformed session id; resuming cold\n", path)
		return ""
	}
	return id
}
