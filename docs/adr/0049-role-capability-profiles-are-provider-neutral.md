# Role capability profiles are provider-neutral; tier and provider stay a Consumer mapping

The default roster (`lib/roster.nix`, `lib/roster-schema-defaults.nix`) pins
one Anthropic model per role — scout on haiku, worker and the coordinator on
sonnet, reviewer (and its review-axis fan-out, which shares the reviewer's
model by default) on opus, filer opt-in and unpinned by default — with a
per-role default effort (scout medium, reviewer and review-axis high, filer
medium, worker high). Those choices carry no recorded rationale beyond two
issue threads:
#2433 named the reviewer's top tier "the highest-leverage read in the loop,"
and #2386 shipped the effort table without stating what each level is *for*.
Neither says, in capability terms, what each role's work demands, so a
Consumer who cannot or does not want to run Anthropic models has nothing to
map their own provider's lineup onto — the roster field is a bare string, any
value is accepted, and the obstacle is missing documented intent, not a
missing knob. #3418 separately ruled model substitution out of scope for
worker cost-cutting; this ADR does not reopen that, it answers the adjacent
question #3418 left open: what should a Consumer *changing providers*, rather
than *changing cost*, actually do.

**We document a provider-neutral capability profile per role — coordinator,
scout, worker, reviewer, filer, review axis — stating what each role's work
demands in capability terms, with an explicit attribution of the current tier
to capability, cost/volume, or both, and a matching reasoning-effort intent. The
mapping from profile to a specific model and effort value on a non-Anthropic
provider stays a Consumer decision made through the roster's existing
untyped `model`/`effort` fields; spindrift adds no typed tier vocabulary and
no per-Driver default table.**

## Capability profiles

**Coordinator.** Not a roster entry — `MODEL`/`EFFORT` are runtime knobs, not
a `rosterDefaults` row, and issue #2240 pins the default model to
`claude-sonnet-5` specifically so a new Consumer's first run is not on a
weaker tier by omission. It owns turn assembly and every loop-control
decision (advance, retry, stop) against the `Handoff` document (ADR 0046) and
the run-state artifact across N passes — sustained instruction-following
against a long, structured contract, plus reliable follow-through on
whichever subagent or gate result it is handed. Reasoning-effort intent:
sustained deliberate reasoning, not a quick scan — every decision it makes
is load-bearing for the rest of the run. Attribution: **both** — it is the
one role every run passes through, so cost matters, but a coordinator that
mishandles the turn contract corrupts the whole pass, so capability is not
negotiable either.

**Scout** (`lib/roster.nix`). Tools: `Read`, `Bash`, `WebFetch`,
`WebSearch`, `Glob`, `Grep`. Description: "Map relevant files, seams, and
tests; return a structured brief." The scout prompt demands breadth (map
every relevant file, seam, and test, ignore nothing in the change radius)
under a hard citation discipline — every load-bearing claim carries a
verbatim quoted excerpt, not a paraphrase — inside a 60-line output budget.
Reasoning-effort intent: a wide but budget-limited scan, not deep
deliberation; medium effort matches a role whose value is coverage and
accuracy of citation, not depth of inference. Attribution: **largely
volume/cost, with a narrow but real capability floor.** The small tier is
mostly there because scouting runs once per issue and burns tokens on wide
search, not because the task is intellectually hard — but a scout that
cannot search broadly or cite accurately poisons every downstream slice
before a human or a stronger model ever sees the work, so the floor is not
zero.

**Worker** (`lib/roster.nix`). Tools: `Read`, `Bash`, `Edit`,
`Write`, `Glob`, `Grep`. Description: "Implement a scoped slice of work
delegated to it, with full implement-capable tools." The worker prompt
demands tool-use reliability across long edit/write/bash sequences,
discipline under context-window pressure (grep logs instead of reading them
whole, batch determined edits into one patch), and staying inside a scoped
delegation without drifting into unrequested refactors. Reasoning-effort
intent: sustained deliberate reasoning across a multi-step implementation,
not a single-shot answer; high effort matches that. Attribution: **both** —
capability, because it has to actually implement the change correctly, and
cost/volume, because a run dispatches many worker slices and few reviewer or
filer passes. Its failure mode is also the least dangerous of the four
subagent roles: a bad implementation is caught by the downstream check gate
(build, tests, lint) before it ever reaches review, so a Consumer's
substitution risk here is bounded by that gate, not silent.

**Reviewer** (`lib/roster.nix`). Tools: `Read`, `Bash`, `WebFetch`,
`Agent`. Description: "Review the branch diff for spec compliance and coding
standards." The review prompt is explicitly adversarial: "assume it is
guilty until proven correct," default to BLOCK, "hunt every dimension... do
not stop at the first finding," and APPROVE "must be earned." That is
counterfactual and adversarial reasoning under a long-lived diff context —
holding the whole change, the issue, and the coding standard in mind at
once, checking each dimension (correctness, security, standards) to
completion rather than stopping at the first plausible finding.
Reasoning-effort intent: sustained deliberate reasoning, because the task is
finding what a first read misses — the reviewer and the worker are the two
roles `lib/roster-schema-defaults.nix` ships at `high` today, and of the two
the reviewer is the one whose deliberation no downstream gate backstops.
Attribution:
**capability**, not cost or volume — a review runs once (or once per
retry round) per turn, so it is the cheapest role by call count, and #2433's
own stated reason for the top tier is capability ("the highest-leverage read
in the loop"), not throughput.

**Filer** (`lib/roster.nix`). Tools: `Read`, `Bash`, `WebFetch`.
Description: "File issues from a review's non-blocking findings,
best-effort." Its prompt is a fixed procedure — label lookup, a dedup search
against existing issues, then a templated issue body — with no adversarial
judgment call beyond dedup matching. Reasoning-effort intent: a low-ambiguity
templated task; medium (not low) leaves headroom for the dedup search, which
is the one step where the filer has to judge whether two descriptions name
the same problem. Attribution: **near-pure cost.** The schema default model
is empty — filing is opt-in, a Consumer who never sets `FILER_MODEL` gets no
filer in `--agents` at all — and the dogfood pin is haiku, the same tier as
the scout, because templated-output fidelity is a low bar next to review's
adversarial reasoning.

**Review axis** (roster entry **review-axis**, `lib/roster.nix`). Tools:
`Read`, `Bash`, `Glob`, `Grep`.
Description: "Run one axis (Standards or Spec) of the code-review skill's
two-axis fan-out." This is where the actual full-diff read happens in the
baked-skill path — the upstream `/code-review` skill hands each review-axis
agent a single axis (Standards or Spec), a diff command, the relevant
standards sources or the spec text, and a word-capped brief; the reviewer
above it only orchestrates and triages the two axes' findings into a verdict,
it does not re-read the diff itself. Reasoning-effort intent: the same
argument the reviewer profile above makes for its own `high` default — the
axis read is the one no downstream gate backstops, and a shallow axis report
reaches the reviewer as a confident all-clear it has no independent way to
catch. Attribution: **capability**, for the same reason as the reviewer.
It has no schema key of its own: before issue #3447 this fan-out
inherited whatever model the reviewer happened to run on, because it spawned
as the driver's built-in `general-purpose` subagent rather than a roster
entry. The roster entry preserves that inheritance by tracking the reviewer
roster entry's *fully resolved* model, through every build-time surface that
can move it — `models.reviewer`, `byName.reviewer.model`, the deprecated
positional `reviewModel`, and finally `reviewModel`'s own schema default — so
the fan-out follows the reviewer wherever a Consumer puts it, unchanged by
default, while `models."review-axis"` / `byName."review-axis".model` still
diverge it independently per name. The `""` #392 opt-out propagates the same
way: opting the reviewer out drops the fan-out entry with it, rather than
leaving an orphan baked on the schema default. A **dispatch-time**
`REVIEW_MODEL=` / `--review-model` (issue #3171) is a different channel, and
does not move the fan-out: it rebinds only the code-owned review pass's own
model on an already-built image, while the baked `--agents` review-axis entry
still carries the model the image was built with — moving the fan-out needs a
rebuild. When a run provisions no `review-axis` agent at all (an explicit
Consumer roster omitting it, or the `""` opt-out), the baked anchor's
`${REVIEW_FANOUT_AGENT}` resolves to the driver's `general-purpose` default
rather than naming an agent type the session never defines. What changed:
the fan-out was previously ungoverned entirely — not a roster entry at all,
so no roster model, effort, or tool restriction ever reached it, and
`pass_usage` attributed its cost under the driver's generic default rather
than a named role.

## What the reviewer's top tier is buying, and what to do without it

The reviewer's failure mode is the one the loop cannot catch by construction:
a weak reviewer's characteristic mistake is a confident APPROVE on a diff it
did not really check, or a fabricated finding that costs a fix round it
never needed. Both are silent. The worker's mistakes surface at the check
gate (build/test/lint failures are loud and blocking); the scout's mistakes
surface as a visibly thin or miscited brief a coordinator can discount; the
reviewer's mistakes surface only as a merged defect or a wasted round, after
the point where anything downstream re-checks its work. **The reviewer's
top-tier assignment is load-bearing** — it is not a cost/volume artifact like
the scout's or filer's tier, and substituting a weaker model into that role
is not a like-for-like trade the way it might be for a worker slice.

A Consumer who cannot field a top-tier model on their provider should not
treat the reviewer as an ordinary substitution target. The options in
descending order of preference: (1) run the strongest model the provider
offers specifically for the reviewer role, even if every other role runs a
cheaper tier — the roster lets model choice vary per role for exactly this
reason; (2) if no tier on the provider is trustworthy for adversarial review,
lean harder on the human review step the loop already has (spindrift's
review pass is not the only gate, just the automated one) rather than
lowering the bar and trusting the automated verdict less than the pipeline
assumes; (3) raise `REVIEW_EFFORT`/the reviewer roster effort to the
provider's own maximum as a partial compensator for a weaker model — it
does not recover capability the model does not have, but it buys more
deliberation out of what capability it does have, which is a smaller version
of the same "review is the highest-leverage read" case #2433 already made
for the top-tier model choice.

## Reasoning effort travels as intent, not a shared vocabulary

`EFFORT`/`REVIEW_EFFORT` (`lib/env-schema.nix`) are
pass-through only — no validation, no normalization. Claude accepts a fixed
ladder (`low`/`medium`/`high`/`xhigh`/`max`) appended as `--effort`; opencode
accepts a provider-specific set appended as `--variant`; an unset value
leaves the Driver's own default in place on either. The capability profiles
above therefore state an effort *intent* — "budget-limited scan," "sustained
deliberate reasoning," "low-ambiguity templated task" — not a level, and a
Consumer maps that intent onto whatever their Driver and provider accept,
exactly as they already map a capability tier onto a model string. This is
consistent with the roster's shape: `effort` and `model` are sibling
untyped fields on the same row, and spindrift already treats one as a pure
Consumer mapping — treating the other one the same way is not a new pattern,
it is the existing pattern applied twice.

## Considered Options

**A provider-neutral tier vocabulary as a typed roster field (e.g.
`tier = "small" | "mid" | "top"`), resolved to a concrete model per Driver
or provider.** Rejected. It would require spindrift to maintain a
model-tier table per provider it does not control and cannot verify — a
table every provider's own model refresh silently invalidates, with nothing
spindrift owns to test it against, multiplied by every provider a Consumer
brings. It would
also duplicate the effort knob's own settled shape: effort is already a
pass-through the Consumer maps by hand, and a typed tier would be the one
inconsistent knob that isn't. Reopen trigger: spindrift takes on
maintaining first-class support for a specific second provider, at which
point a tier table for that one provider (not a universal vocabulary) might
pay for itself.

**Per-Driver default model sets (an opencode-flavored `rosterDefaults`
alongside the existing claude-flavored one).** Rejected. `rosterDefaults`
bakes defaults for `defaultRoster` only, and a custom roster already gets no
injected defaults unless a Consumer sets them explicitly — the mechanism a
second default set would need already exists and is already opt-in. A baked
opencode default set would go stale exactly as fast as a hand-typed
provider list, and unlike the claude defaults it has no first-party issue
thread (#2433, #2386) recording why each value was chosen, since spindrift
does not run opencode as its own dogfood Driver today. Reopen trigger: an
opencode (or other second-Driver) dogfood loop analogous to the current
claude one, which would generate the same kind of recorded rationale the
claude defaults have.

**A normalizing effort-vocabulary layer that maps a shared intent scale onto
each Driver's own ladder automatically.** Rejected. It would have to
enumerate every provider's effort/variant ladder to translate correctly, and
would silently mistranslate on any provider it does not know about — the
same failure shape a typed tier field would have, at a knob that is
currently a clean, honest pass-through. The capability profiles' effort
*intent* language gives a Consumer the same guidance without spindrift
owning a translation table it cannot keep correct.

**Downgrade the reviewer's default tier to match the worker/coordinator mid
tier, on the reasoning that a review runs no more often than a worker
slice.** Rejected. Review's failure mode is silent (a confident wrong
APPROVE, or a fabricated finding) where the worker's is loud and gated by
CI; the roles are not equivalent substitution targets just because they
share a call frequency, and #2433's "highest-leverage read in the loop"
already argued this as a capability floor, not a cost/volume choice.

## Consequences

- A Consumer switching providers now has a per-role capability
  statement, an explicit capability/cost attribution, and a reasoning-effort
  intent to map onto their own model lineup and Driver's effort vocabulary
  — using the roster's existing untyped `model`/`effort` fields, no new
  configuration surface.
- The reviewer's top-tier assignment is documented as a capability floor,
  not a cost artifact, so a future cost-focused pass (in the spirit of
  #3418) has a documented reason not to fold the reviewer into the same
  discount as the worker.
- No schema, roster, or Driver code changes; `lib/roster.nix`,
  `lib/roster-schema-defaults.nix`, `lib/env-schema.nix`, and
  `lib/default-model-fixture.nix` are unchanged by this ADR.
- A future ADR proposing a typed tier vocabulary or a per-Driver default set
  has two named reopen triggers to satisfy first: first-party support for a
  specific second provider, or a second dogfood loop on a non-claude Driver.
