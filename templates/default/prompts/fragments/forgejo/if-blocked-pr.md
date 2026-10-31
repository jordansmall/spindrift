2. Check whether a PR already exists on this branch (`fj pr status`). If not,
   open one as a draft with `fj pr create --base ${BASE_BRANCH} --head ${BRANCH} --body "<summary>" "WIP: <conventional title>"`
   (the title is positional and last; the `WIP: ` prefix is what opens it as
   a draft). If it does, leave it as-is — the Driver never flips a PR to
   ready, so it is already draft and there is nothing to revert. Do NOT use
   `gh` for the pull request — it targets GitHub, not this Forgejo instance.
