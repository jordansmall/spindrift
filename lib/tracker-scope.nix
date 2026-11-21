# The Tracker scope (ADR 0075 decision 3): the slice of a tracker that core
# lists issues from. It is derived, never a knob: the project key on a row that
# names one (`trackerScopeKnob`), the repo slug elsewhere.
{ lib }:
{
  of =
    { row, mergedDefaults }:
    if row ? trackerScopeKnob then
      mergedDefaults.${row.trackerScopeKnob} or ""
    else
      mergedDefaults.repoSlug or "";

  # On a `bareRefClosesIssues` row the tracker is the repo's own issue list, so
  # a scope other than the repo slug would let a bare `Closes #N` in this repo's
  # PR close the same-numbered issue of another repo. Returns true so mkHarness
  # can `assert` it.
  # The assert holds even where no scope knob exists yet, so a row that gains
  # one cannot diverge silently.
  sameRepoOk =
    {
      row,
      scope,
      repoSlug,
    }:
    if (row.bareRefClosesIssues or false) && scope != repoSlug then
      throw "mkHarness: ISSUE_TRACKER=${row.name} has Tracker scope \"${scope}\" but REPO_SLUG is \"${repoSlug}\" -- on ${row.name} the scope must equal the repo slug, because a bare `Closes #N` in this repo's PR would otherwise close the same-numbered issue in another repo"
    else
      true;
}
