- `fj pr status` — the open PR's mergeability and CI rollup on the Forgejo
  instance.
- For the failing job's log, open the run from the PR's checks in the Forgejo
  Actions UI, or curl the REST API against
  `${FORGEJO_BASE_URL:-https://codeberg.org}/api/v1/repos/${REPO_SLUG}/...` —
  the actual failure, not a guess.
