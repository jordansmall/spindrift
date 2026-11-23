# One tracker scope feeds many Target repos through an Issue route

Status: proposed

## Context

Spindrift pairs one Issue Tracker scope with one Target repo. On GitHub
the two are the same repo. ADR 0013 made the tracker and the Code Forge
independent axes, so a Jira project can pair with a GitHub repo, but the
pairing is still one to one: one `JIRA_PROJECT_KEY`, one `REPO_SLUG`, one
Daemon per checkout.

Jira teams rarely work that way. A team usually owns one Jira project
that holds tickets for several repos, with a Component (or a label)
saying which repo a ticket belongs to. Running one spindrift instance per
repo already works, but every instance polls the whole project. Today
nothing stops repo A's instance from claiming repo B's ticket and opening
a PR against the wrong code.

Facts that shape the decision:

- **Selection is a fixed query.** The Jira adapter and Connector both
  build `project = KEY AND (status = … OR labels = …) AND statusCategory
  != Done`. No knob narrows it. The Connector protocol's `Query` already
  carries core-resolved selectors (`Label`, `Status`) that each
  Connector translates into native syntax.
- **Every tracker ANDs labels natively.** `gh issue list --label`,
  Forgejo's `labels=` and JQL `labels =` all do. Forgejo and `local` have
  no query language, so an opaque filter string cannot be generic.
- **The claim is not atomic.** A Jira claim is read-then-write, so two
  instances whose slices overlap can both claim one ticket.
- **Findings dedup reads the whole scope.** The backlog dedup index lists
  every finding-labelled issue in the tracker scope. Dedup keys are
  mostly repo-relative sites (`cmd/main.go:42`), so one repo's closed
  finding would suppress another repo's real one forever.
- **`Target.Repo` means two things.** The Connector protocol documents it
  as the tracker repository, but core fills it from `REPO_SLUG`. For Jira
  that is the wrong value; it should be the project key.
- **Readiness keys off closed, not `agent-complete`.** A blocker is
  satisfied when its agent PR has merged on this Code Forge or its issue
  is closed. Under the default `MERGE_MODE=manual`, `agent-complete` (or
  the mapped `complete` status) is applied when the PR opens green, not
  when it merges. The Jira adapter implements no close.

## Decision

1. **An Issue route narrows one instance's slice of the tracker scope.**
   It is a typed selector set on `Query`, beside `Label` and `Status`:
   kind and value pairs that must all match. `label` is the one kind
   every tracker must support. A tracker declares any extra kinds on its
   backend row; Jira declares `component`. Each declared kind has both a
   filter op and a stamp op. An `mkHarness` assert rejects a kind the
   configured tracker has not declared.
2. **One optional knob per instance.** `issues.route` (env
   `ISSUE_ROUTE`) is an attrset whose names are kinds:
   `{ component = "orders-api"; }`. Unset means no narrowing, which is
   today's behaviour exactly. Each repo's own Consumer flake carries only
   its own route. There is no shared routing table; keeping routes
   disjoint is a documented Consumer rule.
3. **Tracker scope is an internal core value.** Core derives it
   (`JIRA_PROJECT_KEY` on Jira, `REPO_SLUG` elsewhere) and fills the
   Connector `Target.Repo` from it. No knob exposes it yet. An assert
   keeps scope equal to `REPO_SLUG` on `github` and `forgejo`, because a
   bare `Closes #N` in another repo's PR would close the wrong issue.
4. **An instance reads and writes only its slice.** The route is ANDed
   into every tracker list: each Dispatch kind's queue, the Daemon's
   demand count, and the findings backlog index. Every issue the host
   files carries the route's stamp (a label, or Jira's
   `fields.components`), so a promoted finding routes back to the repo
   that found it and dedup sees it.
5. **Readiness is unchanged.** A blocker in another route is satisfied
   when its issue closes, since this instance's Code Forge cannot see
   that repo's PR. Closing a Jira ticket on merge is left to Jira: its
   GitHub integration links the agent branch by the key in its name, and
   an Automation rule or a human moves the ticket to Done.
6. **A startup probe checks a set route.** The Daemon runs the composed
   query once and fails fast if the tracker rejects it, logging the match
   count so an empty slice shows. It warns, without failing, when Jira's
   `complete` mapping is a Done-category status, because that releases
   cross-route dependents before their PR merges.
7. **Selective dispatch confirms an off-route issue.** An issue named
   explicitly that falls outside the route is confirmed the way an
   unlabelled one is: a prompt on a terminal, a refusal without one,
   `--yes` to override.
8. **One ticket, one Target repo.** With a route set, a prompt fragment
   names the Box's Target repo and tells the agent that a spec needing
   changes elsewhere halts through the pre-implement ambiguous-spec gate,
   asking for the ticket to be split into routed, linked tickets.

## Considered Options

- **An opaque native filter string** (raw JQL). Rejected. Forgejo and
  `local` have nothing to compose it into, a typo is a valid query that
  matches nothing, and core cannot stamp or reason about it.
- **Core post-filters normalized issues.** Rejected. Every poll would
  list the whole scope, the demand count would become a list, and each
  tracker would still have to normalize components.
- **A shared routing table every instance reads.** Deferred to the
  multi-repo Daemon, which needs it anyway. It would allow an ambiguity
  halt (a ticket matching two routes) and a report of unrouted tickets.
  Each instance's route then becomes one row of it.
- **Stamp without scoping dedup, or scope without stamping.** Rejected.
  The first leaves cross-repo suppression in place. The second hides an
  instance's own findings from it, so it refiles them every run.
- **Treat `agent-complete` as a satisfied cross-route blocker.**
  Rejected. Under `manual` it fires while the PR still awaits review.
- **A host-observed close on merge.** Deferred. It would promote the
  late-merge sweep from stats to closing tickets, which needs it to run
  on the Daemon's idle tick and drop its 14-day window.
- **Fail hard on a Done-category `complete` mapping.** Deferred to a
  warning until routed deployments show whether teams rely on it.
- **Let the agent build its repo's half of a cross-repo ticket.**
  Rejected. The ticket could then close with the other half tracked
  nowhere.

## Consequences

- A Jira team runs one instance per repo against one project, each with
  its own route, and they dispatch concurrently without claiming each
  other's tickets.
- One instance still serves exactly one Target repo. A Daemon serving
  several repos remains future work.
- A ticket that matches no route sits unclaimed, and nothing reports it.
  [The docs](../reference.md#routed-deployments) tell routed teams to give
  every ticket a routing value.
- Overlapping routes can still double-claim a ticket, because the claim
  is not atomic.
- The Connector protocol's `Query` gains an optional route field and
  `PostIssue` gains the stamp. A Connector that has not declared a kind
  never receives it, since the assert refuses the Consumer first.
- The Jira prompt's `Closes #ENG-123` instruction does nothing on GitHub.
  That is a separate single-repo bug, not part of this decision.
