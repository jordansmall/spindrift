package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/gitexec"
)

// SeedMirror makes dir a bare mirror of every branch on remoteURL, HEAD at
// baseBranch: the per-Pass mirror a read-only Box clones from (issue #6898),
// because that Box holds no credential for the remote. All heads, not just the
// base, so an agent branch already on the remote reaches the Box for branch
// recovery. A userinfo token in remoteURL is presented out of band, never on
// argv or in the mirror's config.
func SeedMirror(remoteURL, baseBranch, dir string) error {
	cleanURL, cred := gitexec.SplitCredential(remoteURL)
	redacted := forge.RedactURLCredentials(cleanURL)

	ctx, cancel := context.WithTimeout(context.Background(), defaultCloneTimeout)
	defer cancel()
	run := func(step string, args ...string) error {
		cmd := cred.Apply(exec.CommandContext(ctx, "git", args...))
		if out, err := cmd.CombinedOutput(); err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("%s: timed out after %s: %w", step, defaultCloneTimeout, ctx.Err())
			}
			return fmt.Errorf("%s: %w: %s", step, err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	if _, err := os.Stat(filepath.Join(dir, "HEAD")); os.IsNotExist(err) {
		if err := run("init mirror", "init", "--bare", dir); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("stat mirror %s: %w", dir, err)
	}
	fetch := gitexec.GuardedArgs(dir,
		"fetch", "--prune", "--force", cleanURL, "+refs/heads/*:refs/heads/*")
	if err := run("fetch "+redacted, fetch...); err != nil {
		return err
	}
	// symbolic-ref succeeds on a missing branch and leaves HEAD dangling, which
	// would only surface as a failed clone inside the Box.
	baseRef := "refs/heads/" + baseBranch
	if err := run("verify base branch", "-C", dir, "rev-parse", "--verify", "--quiet", baseRef+"^{commit}"); err != nil {
		return fmt.Errorf("base branch %q is not on the remote %s", baseBranch, redacted)
	}
	return run("set mirror HEAD", "-C", dir, "symbolic-ref", "HEAD", baseRef)
}
