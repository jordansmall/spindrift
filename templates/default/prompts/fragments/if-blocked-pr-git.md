2. Check whether a PR already exists on this branch (`gh pr view --json url`).
   If not, open one as a draft (`--draft`). If it does, leave it as-is — the
   Driver never flips a PR to ready, so it is already draft and there is
   nothing to revert.
