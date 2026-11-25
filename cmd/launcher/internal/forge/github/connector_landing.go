package github

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/git"
	"spindrift.dev/launcher/internal/gitexec"
)

// gitCredential is the credential core's own git presents to the Connector's
// clone URL under row. It is read from the row's env at each operation, so a
// rotated token is honoured (the Ledger site reads it per git call), and an
// unset one fails here by name rather than reaching git as an empty password.
func gitCredential(row backendrows.Row) (gitexec.Credential, error) {
	token := os.Getenv(row.CredentialEnv)
	if token == "" {
		return gitexec.Credential{}, errors.New(row.CredentialEnv + " is not set")
	}
	return gitexec.Credential{Username: "x-access-token", Password: token}, nil
}

// coreGit is the plumbing a routed row composes over the Connector's clone
// URL: the stock git adapter, except that Rebase honours SYNC_METHOD. It keeps
// the validated URL and credential so core's own clones (the Box's mirror, the
// sync) use exactly what Compose vetted.
type coreGit struct {
	forge.CodeForge
	e        *execClient
	cloneURL string
	cred     gitexec.Credential
}

// clone is the clone step syncBranch takes: core's git, the credential on the
// environment, never the URL, argv or the clone's config.
func (g *coreGit) clone(dir string) error {
	args := append([]string{"clone"}, gitexec.NoAutoMaintenance("--config")...)
	args = append(args, "--no-single-branch", g.cloneURL, dir)
	if out, err := runRedacted(g.cred.Apply(exec.Command("git", args...), g.cloneURL)); err != nil {
		return fmt.Errorf("github: rebase: git clone %s: %w: %s", forge.RedactURLCredentials(g.cloneURL), g.cred.Explain(err, []byte(out), g.cloneURL), out)
	}
	return nil
}

// Rebase syncs head onto core's base branch: the Connector's PR read carries
// the head only.
func (g *coreGit) Rebase(head string) error {
	base := g.e.baseBranch
	for _, ref := range []string{head, base} {
		if ref == "" || strings.HasPrefix(ref, "-") {
			return fmt.Errorf("github: rebase: invalid git ref %q", ref)
		}
	}
	return g.e.syncBranch(head, base, g.clone, g.cred, g.cloneURL)
}

// seedMirror bare-clones the Connector's repository for the Box's mirror. The
// pins are command-scope, so nothing persists into a repo the Box mounts.
func (g *coreGit) seedMirror(dir string) error {
	args := append(gitexec.NoAutoMaintenance("-c"), "clone", "--bare", g.cloneURL, dir)
	if out, err := runRedacted(g.cred.Apply(exec.Command("git", args...), g.cloneURL)); err != nil {
		return fmt.Errorf("github: seed mirror: git clone %s: %w: %s", forge.RedactURLCredentials(g.cloneURL), g.cred.Explain(err, []byte(out), g.cloneURL), out)
	}
	return nil
}

// forgeWrite runs fn over the Connector's full Code Forge, with core's own git
// doing every git operation. Nothing native sits under it: a surface the
// Connector does not declare must fail with errNoCapability, never reach gh.
func (e *execClient) forgeWrite(row backendrows.Row, verb string, fn func(forge.CodeForge, forge.Capabilities) error) error {
	return e.forgeGit(row, verb, func(cf forge.CodeForge, caps forge.Capabilities, _ *coreGit) error {
		return fn(cf, caps)
	})
}

// forgeGit is forgeWrite for the callers that also run git of their own over
// the clone URL Compose validated.
func (e *execClient) forgeGit(row backendrows.Row, verb string, fn func(forge.CodeForge, forge.Capabilities, *coreGit) error) error {
	return e.connectorOp(row, connectorforge.ErrNotForge, verb, func(conn *connectorhost.Connector, api string) error {
		cred, err := gitCredential(row)
		if err != nil {
			return err
		}
		var plumbing *coreGit
		cf, caps, err := connectorforge.Compose(conn, connectorforge.Target{API: api, Repo: e.repo}, connectorforge.Git{
			Credential: cred,
			// The Routes name only api.github.com; git and the web are on the row's
			// declared Git remote, which core derives the clone URL and web host from.
			WebBase: row.GitRemote,
			Plumbing: func(cloneURL string, cred gitexec.Credential) forge.CodeForge {
				plumbing = &coreGit{
					CodeForge: git.NewGitClient(cloneURL, e.baseBranch, e.userName, e.userEmail, git.WithCredential(cred)),
					e:         e, cloneURL: cloneURL, cred: cred,
				}
				return plumbing
			},
		})
		if err != nil {
			return err
		}
		return fn(cf, caps, plumbing)
	})
}

// prWrite is forgeWrite for the writes on forge.PRForge.
func (e *execClient) prWrite(row backendrows.Row, verb string, fn func(forge.PRForge) error) error {
	return e.forgeWrite(row, verb, func(_ forge.CodeForge, caps forge.Capabilities) error {
		if caps.PRForge == nil {
			return fmt.Errorf("%w: forge.prs", errNoCapability)
		}
		return fn(caps.PRForge)
	})
}

func (e *execClient) markReady(row backendrows.Row, prURL string) error {
	return e.prWrite(row, "mark a PR ready", func(pf forge.PRForge) error { return pf.MarkReady(prURL) })
}

func (e *execClient) markDraft(row backendrows.Row, prURL string) error {
	return e.prWrite(row, "mark a PR draft", func(pf forge.PRForge) error { return pf.MarkDraft(prURL) })
}

func (e *execClient) commentPR(row backendrows.Row, prURL, body string) error {
	return e.prWrite(row, "comment on a PR", func(pf forge.PRForge) error { return pf.CommentPR(prURL, body) })
}

func (e *execClient) deleteBranch(row backendrows.Row, branch, base string) error {
	return e.forgeWrite(row, "delete a branch", func(_ forge.CodeForge, caps forge.Capabilities) error {
		if caps.BranchDeleter == nil {
			return fmt.Errorf("%w: forge.branches", errNoCapability)
		}
		return caps.BranchDeleter.DeleteBranch(branch, base)
	})
}

func (e *execClient) createDraftPR(row backendrows.Row, title, body, base, head string) (url string, created bool, err error) {
	err = e.forgeWrite(row, "open a draft PR", func(_ forge.CodeForge, caps forge.Capabilities) error {
		if caps.DraftPRCreator == nil {
			return fmt.Errorf("%w: forge.draftPRs", errNoCapability)
		}
		var cerr error
		url, created, cerr = caps.DraftPRCreator.CreateDraftPR(title, body, base, head)
		return cerr
	})
	return url, created, err
}

func (e *execClient) pushBranch(row backendrows.Row, srcDir, localRef, branch, base string) error {
	return e.forgeWrite(row, "push a branch", func(_ forge.CodeForge, caps forge.Capabilities) error {
		if caps.BranchPusher == nil {
			return fmt.Errorf("%w: forge.draftPRs", errNoCapability)
		}
		return caps.BranchPusher.PushBranch(srcDir, localRef, branch, base)
	})
}

func (e *execClient) relayBundle(row backendrows.Row, outboxDir, ref string) error {
	return e.forgeWrite(row, "relay a bundle", func(_ forge.CodeForge, caps forge.Capabilities) error {
		if caps.BundleRelay == nil {
			return fmt.Errorf("%w: forge.draftPRs", errNoCapability)
		}
		return caps.BundleRelay.RelayBundle(outboxDir, ref)
	})
}

func (e *execClient) commitSubjects(row backendrows.Row, outboxDir, base, ref string) (subjects []string, err error) {
	err = e.forgeWrite(row, "read a bundle's commit subjects", func(_ forge.CodeForge, caps forge.Capabilities) error {
		if caps.BundleCommitSubjects == nil {
			return fmt.Errorf("%w: forge.draftPRs", errNoCapability)
		}
		var serr error
		subjects, serr = caps.BundleCommitSubjects.CommitSubjects(outboxDir, base, ref)
		return serr
	})
	return subjects, err
}

func (e *execClient) rebasePRHead(row backendrows.Row, prURL, head string) error {
	return e.forgeWrite(row, "rebase a PR", func(_ forge.CodeForge, caps forge.Capabilities) error {
		if caps.PRHeadRebaser == nil {
			return fmt.Errorf("%w: forge.prs", errNoCapability)
		}
		return caps.PRHeadRebaser.RebasePRHead(prURL, head)
	})
}

func (e *execClient) seedMirror(row backendrows.Row, dir string) error {
	return e.forgeGit(row, "seed the mirror", func(_ forge.CodeForge, _ forge.Capabilities, g *coreGit) error {
		return g.seedMirror(dir)
	})
}

// ledgerSite is the butler Ledger's remote under a routed row: the
// clone URL Compose validated and the row's credential, so the Ledger's git
// carries no gh credential helper. The credential is a source, read by each
// Ledger git call: the site outlives a token refresh.
func (e *execClient) ledgerSite(row backendrows.Row) (backendkit.LedgerSite, error) {
	var site backendkit.LedgerSite
	err := e.forgeGit(row, "reach the Ledger", func(_ forge.CodeForge, _ forge.Capabilities, g *coreGit) error {
		// forgeGit already resolved the credential once, so an unset one fails
		// here at build; each Ledger git call resolves it afresh.
		site = backendkit.LedgerSite{URL: g.cloneURL, Credential: func() (gitexec.Credential, error) { return gitCredential(row) }}
		return nil
	})
	return site, err
}

// ErrRoutedMergeMethod is why a routed github row refuses a MERGE_METHOD
// other than rebase: the Connector always merges by rebase, so honouring the
// knob is impossible and ignoring it would be silent.
var ErrRoutedMergeMethod = errors.New("MERGE_METHOD must be rebase or unset: the github Connector merges by rebase only")

func (e *execClient) checkRoutedMergeMethod() error { return checkRoutedMergeMethod(e.mergeMethod) }

func checkRoutedMergeMethod(method string) error {
	if method != "" && method != "rebase" {
		return fmt.Errorf("%w (got %q)", ErrRoutedMergeMethod, method)
	}
	return nil
}

// merge merges the PR, then deletes its head branch as gh's
// --delete-branch does. The delete is best effort: the PR has landed by then,
// and failing the merge would turn it into a failed settle. A head already gone
// (a repository that deletes merged heads itself) is done, not a fault, so it
// stays out of the Connector log; any other failure is recorded there and warned.
func (e *execClient) merge(row backendrows.Row, prURL string) error {
	if err := e.checkRoutedMergeMethod(); err != nil {
		return err
	}
	err := e.forgeWrite(row, "merge a PR", func(cf forge.CodeForge, caps forge.Capabilities) error {
		// A push-only Connector composes without a PR forge; its Merge must
		// not be reachable.
		if caps.PRForge == nil {
			return fmt.Errorf("%w: forge.prs", errNoCapability)
		}
		return cf.Merge(prURL)
	})
	if err != nil {
		return err
	}
	err = e.forgeWrite(row, "delete a merged PR's head branch", func(cf forge.CodeForge, caps forge.Capabilities) error {
		head, crossRepo, herr := connectorforge.HeadOf(cf, prURL)
		if herr != nil {
			return herr
		}
		// `gh pr merge --delete-branch` leaves a cross-repository PR's head
		// alone; deleting by name here would hit a same-named base-repo
		// branch instead.
		if crossRepo {
			return nil
		}
		if caps.BranchDeleter == nil {
			return fmt.Errorf("%w: forge.branches", errNoCapability)
		}
		if derr := caps.BranchDeleter.DeleteBranch(head, e.baseBranch); connectorhost.ErrorClass(derr) != connectorhost.NotFound {
			return derr
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "merged %s but did not delete its head branch: %v\n", prURL, err)
	}
	return nil
}

func (e *execClient) enqueueAutoMerge(row backendrows.Row, prURL string) error {
	if err := e.checkRoutedMergeMethod(); err != nil {
		return err
	}
	return e.prWrite(row, "enqueue auto-merge", func(pf forge.PRForge) error { return pf.EnqueueAutoMerge(prURL) })
}
