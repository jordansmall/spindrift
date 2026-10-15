# Judging rubric

You are reviewing one candidate patch written to resolve the issue below. You
are given the issue, a reference fix that was merged for it, and the
candidate patch. The reference shows one correct way to resolve the issue; it
is not the answer key. Other approaches may be equally or more correct, and a
candidate must not be penalised for differing from the reference. Judge the
candidate against the issue and the codebase it changes, using the reference
only to understand what the issue needed and which edge cases mattered.

Score the candidate on each dimension from 1 to 5. Use the anchors below; 2
and 4 sit between them.

## correctness

Does it fix the issue fully, handle the edge cases the issue implies, and keep
existing behaviour working?

- 1: does not fix the issue, fixes the wrong thing, or breaks existing
  behaviour.
- 3: fixes the main case the issue describes but misses an edge case it
  implies, or risks a regression elsewhere.
- 5: fixes the issue completely, edge cases included, with no regression of
  existing behaviour.

## tests

Do new or changed tests pin the behaviour the issue cares about, so they would
fail without the fix?

- 1: no tests for the change, or tests that would pass without the fix.
- 3: tests cover the main case but miss an edge case, or assert something
  weaker than the behaviour the issue cares about.
- 5: tests pin the behaviour the issue cares about, edge cases included, and
  would fail without the fix.

## scope

Is the change focused, with docs updated where the repo documents the
behaviour, and no unrelated churn?

- 1: large unrelated changes, or user-facing behaviour changed with its docs
  left stale.
- 3: mostly focused, but with some unrelated churn or a doc the change should
  have touched left as it was.
- 5: touches only what the issue needs, and updates every doc that describes
  the changed behaviour.

## quality

Are comments accurate, and is the code readable, idiomatic, and consistent with
the surrounding code?

- 1: misleading or stale comments, hard to follow, or at odds with the
  surrounding code's conventions.
- 3: readable and correct, but with awkward structure, redundant comments, or
  small departures from the surrounding style.
- 5: clear, idiomatic, matches the surrounding code, and every comment is
  accurate and earns its place.

## overall

Score the candidate from 1 to 10 as a whole: how confident you would be
merging it as it stands. 1 is unusable, 5 needs substantial rework, 8 is
mergeable after small nits, 10 is as good as or better than the reference.

## Answer

Reply with a single JSON object and nothing else: no prose, no code fence.
The rationale is one sentence naming the main reason for the scores.

{"correctness":n,"tests":n,"scope":n,"quality":n,"overall":n,"rationale":"..."}
