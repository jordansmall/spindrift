package doctor

import "fmt"

// RepoNameCheck warns when REPO_SLUG differs from the name the code forge
// reports for the repository. Advisory: the daemon preflight reads doctor's
// exit code, so a rename must warn, never fail. Compared with != because git's
// path match is case-sensitive.
func RepoNameCheck(canonical, slug string) Check {
	return Check{
		Name: "repo-name",
		Tier: Advisory,
		Probe: func() (any, error) {
			if canonical != slug {
				return nil, fmt.Errorf("code forge names the repository %q, but REPO_SLUG is %q", canonical, slug)
			}
			return canonical, nil
		},
		Remedy: fmt.Sprintf("update REPO_SLUG / --repo-slug / forge.repoSlug to %s", canonical),
		SuccessMsg: func(output any) string {
			return fmt.Sprintf("repository name %s matches the code forge's canonical name", output.(string))
		},
	}
}
