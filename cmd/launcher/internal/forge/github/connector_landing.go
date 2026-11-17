package github

import (
	"errors"
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/git"
	"spindrift.dev/launcher/internal/gitexec"
)

// gitCredential is the credential core's own git presents to the Connector's
// clone URL under row. It is read from the row's env at each operation, so a
// rotated token is honoured, and an unset one fails here by name rather than
// reaching git as an empty password.
func gitCredential(row backendrows.Row) (gitexec.Credential, error) {
	token := os.Getenv(row.CredentialEnv)
	if token == "" {
		return gitexec.Credential{}, errors.New(row.CredentialEnv + " is not set")
	}
	return gitexec.Credential{Username: "x-access-token", Password: token}, nil
}

// forgeWrite runs fn over the Connector's full Code Forge, with core's own git
// doing every git operation. Nothing native sits under it: a surface the
// Connector does not declare must fail with errNoCapability, never reach gh.
// readOnlyBox is for the Box's bundle relay; the other callers pass false.
func (e *execClient) forgeWrite(row backendrows.Row, verb string, readOnlyBox bool, fn func(forge.CodeForge, forge.Capabilities) error) error {
	return e.connectorOp(row, connectorforge.ErrNotForge, verb, func(conn *connectorhost.Connector, api string) error {
		cred, err := gitCredential(row)
		if err != nil {
			return err
		}
		cf, caps, err := connectorforge.Compose(conn, connectorforge.Target{API: api, Repo: e.repo}, connectorforge.Git{
			Credential:  cred,
			ReadOnlyBox: readOnlyBox,
			Plumbing: func(cloneURL string, cred gitexec.Credential) forge.CodeForge {
				return git.NewGitClient(cloneURL, "", e.userName, e.userEmail, git.WithCredential(cred))
			},
		})
		if err != nil {
			return err
		}
		return fn(cf, caps)
	})
}

// prWrite is forgeWrite for the writes on forge.PRForge.
func (e *execClient) prWrite(row backendrows.Row, verb string, fn func(forge.PRForge) error) error {
	return e.forgeWrite(row, verb, false, func(_ forge.CodeForge, caps forge.Capabilities) error {
		if caps.PRForge == nil {
			return fmt.Errorf("%w: forge.prs", errNoCapability)
		}
		return fn(caps.PRForge)
	})
}

func (e *execClient) markReadyViaConnector(row backendrows.Row, prURL string) error {
	return e.prWrite(row, "mark a PR ready", func(pf forge.PRForge) error { return pf.MarkReady(prURL) })
}

func (e *execClient) markDraftViaConnector(row backendrows.Row, prURL string) error {
	return e.prWrite(row, "mark a PR draft", func(pf forge.PRForge) error { return pf.MarkDraft(prURL) })
}

func (e *execClient) commentPRViaConnector(row backendrows.Row, prURL, body string) error {
	return e.prWrite(row, "comment on a PR", func(pf forge.PRForge) error { return pf.CommentPR(prURL, body) })
}

func (e *execClient) deleteBranchViaConnector(row backendrows.Row, branch, base string) error {
	return e.forgeWrite(row, "delete a branch", false, func(_ forge.CodeForge, caps forge.Capabilities) error {
		if caps.BranchDeleter == nil {
			return fmt.Errorf("%w: forge.branches", errNoCapability)
		}
		return caps.BranchDeleter.DeleteBranch(branch, base)
	})
}

func (e *execClient) createDraftPRViaConnector(row backendrows.Row, title, body, base, head string) (url string, created bool, err error) {
	err = e.forgeWrite(row, "open a draft PR", false, func(_ forge.CodeForge, caps forge.Capabilities) error {
		if caps.DraftPRCreator == nil {
			return fmt.Errorf("%w: forge.draftPRs", errNoCapability)
		}
		var cerr error
		url, created, cerr = caps.DraftPRCreator.CreateDraftPR(title, body, base, head)
		return cerr
	})
	return url, created, err
}
