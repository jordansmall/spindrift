# The merge guard bounds drift, not adversaries

The merge guard — the launcher-side check that downgrades a merge to manual
(with a PR note) when a change touches guarded paths — is deliberately
documented as a control against **injection-induced drift**, not against a
fully adversarial Agent. Under the default single-token design it is
bypassable, and we ship it anyway.

## Context

Once an issue is labeled, its body and every comment from any GitHub user feed
the Agent as prompt input; injection is inherent to the design (see
`docs/reference.md` Threat model). The gate that matters is therefore on the
*output* side: no agent-authored change should land on the base branch without
either a human read or a check the Agent cannot influence. Green CI is not that
check — the Agent writes the tests. Path-based guarding of the sensitive
surface (CI config under `.github/`, and the instruction surface —
`CLAUDE.md`, `AGENTS.md`, `.claude/`, `.opencode/` — whose poisoning would
persist across every future run) closes the highest-value gap.

But the capability to merge does not live in the launcher; it lives in the
GitHub token, which is inside the Box. GitHub's fine-grained permissions cannot
split "open PR" from "merge PR" — both are `Pull requests: write` — and the
Agent legitimately needs Contents RW and Pull requests RW to do its job. A
fully adversarial Agent can `gh pr merge` its own green PR from inside the Box
before the launcher ever evaluates the guard.

## Decision

Ship the merge guard under the single-token design and state the bypass
honestly:

- The guard **downgrades to manual, never blocks** — the cost of a hit is one
  human read, so the default guarded-path set can be broad without making the
  gate noise.
- **SECURITY.md documents the bypass** rather than implying the guard is
  adversary-proof.
- A **tripwire** (launcher detects a PR merged by an actor other than itself
  while the issue is `InProgress`) provides detection without prevention.
- **Two-actor separation** is the documented opt-in hard mode: a repository
  ruleset bars the Box token's user from updating the base branch, and only a
  second, launcher-held token can merge. It is the only configuration in which
  the guard is literally uninfluenceable by the Agent.

## Considered Options

- **Two-actor separation as the default** — two machine accounts, a ruleset,
  and a second secret to plumb is real operator friction for the open-source
  story; the single token is load-bearing for spindrift's setup simplicity.
  Kept as opt-in, not default. Rejected as default.
- **Splitting merge capability off the Box token** — impossible on GitHub;
  fine-grained permissions do not distinguish opening from merging PRs, and
  merging also requires the Contents RW the Agent needs to push branches.
  Rejected as unavailable.
- **No guard, documentation only** — the realistic near-term adversary is a
  confused-but-cooperative Agent following its normal flow under injected
  instructions; the guard genuinely bounds that case, and the guardrail prompt
  tells the model the guarded paths force human review. Rejected.

## Consequences

- The guard's security claim is scoped: it holds against drift through the
  normal flow, and against nothing stronger, unless two-actor separation is
  enabled.
- The default guarded-path set encodes threats the operator hasn't thought of
  (the instruction surface as a cross-run persistence vector) — operators
  extending `MERGE_GUARD_PATHS` add to it, they don't rediscover it.
- Off the `github` Code Forge (push-only `git` forge, `MERGE_MODE=immediate`)
  there is no launcher in the merge path and therefore no guard at all; the
  docs must scope the guard to the `github` merge path explicitly.
