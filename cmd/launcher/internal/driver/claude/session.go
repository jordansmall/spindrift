package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
)

// SessionFlags renders the claude CLI's session flags. The session id is
// derived from repoSlug and issue alone, so no state is stored between runs.
// "resume" renders nothing when that session's transcript is missing under
// home's projects directory (an evicted cache, or a first fix pass after a
// crash), and the caller falls back to the cold-context fix flow.
func SessionFlags(mode, repoSlug, issue, home string) string {
	sum := sha256.Sum256([]byte("spindrift-session:" + repoSlug + ":" + issue))
	h := hex.EncodeToString(sum[:])[:32]
	id := h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
	switch mode {
	case "initial":
		return "--session-id " + id
	case "resume":
		// An empty home globs from the filesystem root, as the in-box
		// ${HOME:-} expansion did.
		// Glob's ErrBadPattern is dropped on purpose: bash's `compgen -G`
		// matched nothing on a bad pattern, so no --resume renders.
		if m, _ := filepath.Glob(home + "/.claude/projects/*/" + id + ".jsonl"); len(m) > 0 {
			return "--resume " + id
		}
	}
	return ""
}
