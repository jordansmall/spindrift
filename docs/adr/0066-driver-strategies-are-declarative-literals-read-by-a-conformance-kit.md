# Driver strategies are declarative literals read by a conformance kit

Status: proposed

## Context

ADR 0065 moved every Pass-log reader out of the Driver strategies and into
driverkit, leaving each strategy a decoder, its classify patterns and
options, its usage totals mode, its exit resolution, result-event encoder and
session flags. Its closing amendment (issue #5187) records what the `claude`
package keeps beyond that: three quirks the shared readers take as inputs.

What the amendment does not say is how a strategy hands those inputs to the
readers. On `main` after #5187 each Driver's registration is a ten-method
type whose every method is a one-line call into driverkit with that Driver's
decoder and inputs. The two registrations differ in four places that are not
in either Driver's package: the default role the heartbeat attributes
top-level messages to, the order of per-model usage rows, whether a non-empty
transcript ends with a newline, and whether the result is every `result`
event in the log or the last assistant message. A third Driver (`pi`, ADR
0009 amendment for issue #4945; ticket #5479) would be a third copy of the ten
methods, choosing each of the four by hand.

The test burden has the same shape. The Go test check is one derivation over
the whole launcher directory, so any edit anywhere reruns every Driver's
corpus goldens, and every Driver's goldens rerun on an edit to any other
Driver. Within the tree, each Driver's tests import the registry package,
which imports every other Driver, so no Driver's tests compile alone. The
corpus runner lives in the registry package and loops over every registered
name. On the Nix side the Driver checks are one file of 67 attrs, 29 of them
naming `claude` or `opencode` by hand, including pairwise parity checks that
compare the two by name.

Three Driver facts are still name switches outside the Driver seam: the
credential rule in launcherchecks (which env vars satisfy a Driver, and when
one is required), the setup command quickstart runs, and the two lists that
mirror the credential names into the Box's Bash scrub list and the bwrap
runner's off-argv map. Two other facts already cross the seam as generated
Go: the Driver names and the Provider-namespaced-models flag, as one
generated list each. The backend family solved the same problem once
(issue #2521): rows in a Nix registry render into one generated Descriptor
per backend, and a test fails when the Go struct and the Nix rows disagree.

## Decision

A Driver's host-side strategy is a **literal**, and the shared code reads it.

1. **`driverkit.Strategy` is the strategy.** One value per Driver holds its
   decoder factory, transient and terminal patterns, classify options,
   usage totals mode, write-tool name, heartbeat tracking hooks, the default
   top-level role, which result reader applies, and the three functions
   `ResolveExit`, `ResultEvent` and `SessionFlags`. The registry builds the
   existing `driver.Driver` interface from it with one shared
   implementation. Core callers keep the interface and see no change.
2. **Two quirks become shared rules.** A non-empty transcript always ends
   with a newline. Per-model usage rows sort by the Strategy's model-family
   hook, then by exact id; a Driver with no family hook sorts by id. The
   goldens that move are `opencode`'s transcript trailing byte and its
   per-model row order.
3. **Two quirks stay Driver facts.** The default top-level role, because
   whether a stream attributes roles is a property of the CLI. And the
   result reader, because the corpus shows the two readers are different
   answers to "what is the result": on multi-pass `claude` logs the
   every-result reader returns each pass's verdict and the outcome line,
   while the last-message reader returns only the last message, and the two
   disagree on 8 of 11 `claude` corpus cases. Unifying them is a behaviour
   change with its own goldens review, not part of this decision.
4. **A conformance kit under driverkit runs a Driver's tests.** It replays
   the corpus goldens, the chunked heartbeat, and the credential guard over
   one Strategy and one corpus directory. Each Driver's corpus moves under
   its own package, each Driver's tests build their Driver from its
   Strategy and never import the registry, and the registry package keeps
   only its own tests and the Nix parity test. The seamtest fake becomes a
   Strategy too, in the second stage below.
5. **One Go test derivation per Driver**, mapped over the Nix registry's
   names, over a fileset of that Driver's package, driverkit, the shared
   packages they import, and that Driver's corpus. A new Driver's row
   yields its derivation with no hand-pinned attr. The core Go test
   derivation drops the Driver packages from its package list.
6. **One Nix check file per Driver**, unioned over the registry's names.
   The generic shape checks and the pairwise parity checks become maps over
   every entry, so "opencode's skills dir matches claude's" reads "every
   Driver's skills dir matches the first".
7. **Driver facts are one generated Descriptor**, rendered from the Nix
   rows the way the backend Descriptor is, replacing both generated name
   lists. Credentials are rule rows on the Nix entry: the env vars that
   satisfy the rule (either-or, in fetch order), an optional model-id prefix
   that makes the rule apply, a remedy hint, and an optional setup command.
   launcherchecks walks the rules; quickstart runs a rule's setup command
   when present; the Box's scrub list and the runner's off-argv map render
   from the same rows on regen. The rules are plain data so they can travel
   in the launcher input document (ADR 0020) should the registry ever open
   (ADR 0067).
8. **Registration inverts in a second stage.** Today every core package
   that reads logs imports the registry, which imports every Driver, so the
   core test derivation's fileset must carry every Driver's sources and a
   decoder edit reruns the core suite regardless of (5). In the second
   stage the registry package stops importing the Driver packages; a small
   bundle package registers the bundled Strategies and only the four
   binaries (launcher, box, driver-exec, orchestrator) import it.
   *Amended by issue #5541:* quickstart and daemon import it too (see the
   amendment below). Core tests build Drivers from a seamtest Strategy
   whose decoder reads canonical events as JSON lines, and the inline `claude` stream-json
   fixtures in core tests (29 files on `main`) become canonical-event
   fixtures. When the last Driver import leaves the core tests, the core
   fileset drops the Driver sources. The first stage lands before `pi`'s
   first implementation ticket; the second has no deadline.

## Considered Options

- **A field per quirk, no behaviour change.** Every golden stays
  byte-identical, but `pi` inherits two Strategy fields that exist only to
  replay `claude` and `opencode` history. Rejected for the cosmetic two;
  it is the shape kept for the semantic two.
- **Normalize all four quirks.** One result reader (every `result` event if
  any, else the last message) might reproduce both Drivers on the corpus,
  but it is a behaviour change on a reader the outcome extraction depends
  on. Deferred behind its own goldens review.
- **Invert registration before `pi`.** Lands `pi` into the final shape but
  delays it by the fixture migration, roughly 29 fixture files and 39 call
  sites. Rejected: the per-Driver derivations give `pi` its own check from
  its first ticket without the migration.
- **Additive split only.** Per-Driver derivations with the registry still
  importing every Driver. Gives parallel runs and a per-Driver signal but a
  decoder edit still reruns core. Kept as the first stage, not the end
  state.
- **A central corpus with a per-Driver runner parameter.** Less file
  movement, but every derivation still compiles every Driver and any corpus
  edit reruns all of them. Rejected.
- **Checks on the shipped Nix row.** Would let a row carry its checks, but
  the lib exports are versioned (VERSIONING.md) and a Consumer never
  evaluates a check. The check files stay in the repo's checks tree.
- **Credentials stay a Go switch.** The requiredness predicate is logic,
  but a prefix match is data the way the backend Descriptor's booleans are,
  and the three hand-maintained name lists are the stronger argument.
- **A third generated list for credentials.** Smallest diff; leaves the
  three hand lists hand-maintained. Rejected.
- **`Spec` or `Profile` as the name.** `spec` already means a
  specification issue in this repo's ticket vocabulary; `Profile` would be
  a new term. The glossary and ADR 0065 already call the Go half a
  strategy, and after this decision the literal is the strategy.

## Consequences

- ADR 0065's worked-example sentence ("the Driver registrations call the
  shared readers directly") is superseded: a registration is a Strategy
  literal and one shared implementation calls the readers.
- `pi`'s first ticket (#5479) writes a Strategy literal and gains
  `driver-pi-go-test` and a `pi` check file; its credential ticket (#5480)
  adds rule rows, not a switch arm. Both wait on the first stage.
- The reference guide's "Host-side event layer" section describes the
  Strategy's fields in place of the two registrations as worked examples,
  and CONTEXT.md gains the Strategy, the conformance kit and the Driver
  Descriptor.
- The two goldens that move (`opencode` transcript newline, per-model row
  order) are regenerated in the same change that introduces the shared
  rule, with the diff reviewed as the proof that nothing else moved.
- Until the second stage lands, a Driver edit still reruns the core Go test
  derivation. The per-Driver derivations are the burden shift's first
  half, not the whole of it.
- The seamtest fake's canonical-event decoder makes the canonical events a
  test-only wire format. That is a step toward, not a decision for, the
  process seam ADR 0067 describes.

## Amendment (issue #5541): the second stage's expand step

The registry package imports no Driver, and the bundle package registers
the bundled Strategies. quickstart and daemon import it too: quickstart
validates `DRIVER` through `driver.New` and would otherwise refuse every
name, and daemon links dispatchrecord's Driver log parsing through chore.
seamtest gains the canonical-event Strategy and a helper that registers it
as `claude` when no Driver of that name is registered. Each
library test package that still exercises a real Driver blank-imports the
bundle in a test file, and an import guard holds those packages on an
allow-list that the per-package migration shrinks.

## Amendment (issue #5545): the contract step

The allow-list is gone. `TestOnlyBinariesReachTheBundle` now forbids any
library package's test closure from reaching the bundle or a Driver
outright; only the binaries and the `seamtest/bundled` fixture may. The core
`launcher-go-test` fileset excludes every Driver package and corpus, the
bundle and the binaries, whose tests run in `launcher-binaries-go-test`. A
Driver edit therefore reruns that Driver's own check and the derivations
built from the whole launcher tree or the launcher binaries, and no longer
the core suite or another Driver's check.
