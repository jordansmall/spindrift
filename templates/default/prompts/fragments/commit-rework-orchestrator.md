Whether this pass is authoring the first slice or committing fixes for a
prior review round's findings is read from run-state, not decided here —
the same seeded "## Run-state handoff" section and its `Last reviewer
verdict:` line the REVIEW section below reads.

- **No handoff section, or a handoff section with no `Last reviewer
  verdict:` line** (e.g. a worker-dispatch manifest continuation, or any
  other carried-forward state short of a resolved review verdict): no
  review round has reached a verdict on this branch yet. The preference
  above of "several small focused commits" stands unchanged — commit
  each logical unit separately as usual.
- **A handoff section with a `Last reviewer verdict:` line** (`APPROVE`
  or `BLOCK`): a review round has already run against this branch, and
  this pass is committing fixes for its findings — blocking ones on
  `BLOCK`, non-blocking ones REVIEW's own triage below decides to fix
  on `APPROVE`. That overrides the preference above of "several small
  focused commits" for this pass: instead,
  fold each fix into the commit it logically belongs to —
  `git commit --amend` for the branch tip, an autosquash fixup for an
  older commit — rather than tacking on a follow-up commit. Add a
  *new* commit only when the fix is genuinely a separate file or scope.

  Complete a fixup immediately, never leave a dangling `fixup!` commit
  on the branch: `git commit --fixup=<sha>`, then
  `GIT_SEQUENCE_EDITOR=true git rebase -i --autosquash origin/${BASE_BRANCH}`
  squashes it in without any human reading or editing anything (the
  sequence editor is a no-op that accepts the autosquash reorder as-is)
  — an unsquashed `fixup!` commit stays its own separate commit on the
  branch, the exact outcome folding exists to avoid.

  You are a fresh session and did not author any of this branch's prior
  commits yourself, but every commit between `origin/${BASE_BRANCH}` and
  `${BRANCH}` is this run's own automated work on this issue — including
  any a prior, crashed run of this same automated attempt already
  committed — so all of them are fair game to amend or fixup
  (`git log origin/${BASE_BRANCH}..${BRANCH}` lists them).

  The branch force-pushes, so rewriting that history here is expected.
