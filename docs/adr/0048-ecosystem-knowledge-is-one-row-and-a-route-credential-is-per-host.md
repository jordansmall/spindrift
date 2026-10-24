# Ecosystem knowledge is one row in one package, and a route's credential is per host

An architecture review of the Registry proxy (2026-09-06) found that ADR
0045's "one table: the ecosystem package" had not held: an ecosystem's
knowledge sat in one row plus five name-keyed side tables across three
packages (discovery's extractor map, the launcher's declared-path list,
the proxy's cargo-only rewrite lookup and index-base filter, and the
credential-store switches), three of them outside the containment test's
reach. We decided the **Ecosystem row is the only home**: every
per-ecosystem fact and hook — lockfiles, committed-config parser,
route-declaration validation, response-rewrite rows, credential store,
bindings — lives on the row, one file per ecosystem in one package, with
an ordered table in the package's root file and a parity test that every
row is in it. (The 2026-10-22 amendment below supersedes the package
layout.) Consumers walk the table; none spells a name. Alongside it,
a **Registry route carries exactly one credential and one auth scheme for
its whole host**, never one per ecosystem.

## Considered options

**One Go package per ecosystem.** Rejected for now. The ecosystems never
call each other, so the package seam would contain nothing; it would
force a three-way split (a leaf for the shared hook and result types, six
subpackages, a table package above them) and make the table's
load-bearing precedence order — cargo, npm family, go, gradle — implicit
in registration or import order. File-per-ecosystem gives the same
"open one file, see everything about cargo" locality. Promote a single
ecosystem to its own package only on a trigger: a dependency scoped to it
(e.g. a YAML library only yarn and pnpm need) or a file that has outgrown
itself. The row-value shape makes that promotion mechanical.

**A Go interface with optional capability interfaces** (the `io.ReaderFrom`
pattern). Rejected: it names each hook as a contract but turns the table's
plain data into methods and puts a type assertion in every reader, for no
gain in leverage over a struct of nil-able hook fields, which already
encodes "no such notion" for this ecosystem.

**A per-ecosystem credential under `[routes.ecosystems.<name>]`, with the
route credential as fallback.** Rejected. The client-side credential
shapes that differ by toolchain (npm's base64 `_auth`, cargo's bearer
token, gradle's user and password) never reach the routes file: no
toolchain in the Box holds a credential, and the proxy renders one header
per route on the outbound leg. What matters is what the upstream host
accepts, and universal registries (Artifactory, Nexus, GitLab) accept one
scheme host-wide, while registries that split scheme by ecosystem
(GitHub Packages) also split by host, which is already a separate route.
Adding it would give the proxy a second credential-selection axis (by
admitted subtree) and weaken ADR 0045's load-bearing "credential and host
bound in the same record". Reopen trigger: a Consumer whose single host
demands two auth schemes or two token values by ecosystem. The
ecosystem-keyed sub-table grammar leaves room for a `credential` key
there without a migration.

## Consequences

- The routes file's per-ecosystem keys (`go-path`, `gradle-path`,
  `cargo-registries`) become one `[routes.ecosystems.<name>]` block with
  one typed key, `path`; the row validates any further keys. The retired
  keys are refused at the launch gate with the equivalent block printed,
  as `upstream-base-url` was.
- The proxy takes its response-rewrite table as a constructor input, wired
  from the rows by the launcher, so the proxy's import set stays free of
  the committed-config parsers that now live on the rows. The npm
  packument row ships with the mechanism, closing the documented tarball
  gap.
- A dependency-free leaf package owns the vocabulary every hop shares
  (host key, tagged subtree, path-set admission, rewrite row types,
  header-name validation), replacing the copies that were kept in sync by
  convention.
- The containment scan covers every package that imports the ecosystem
  package plus the launcher's main package (amended by issue #5748: it also
  covers every direct importer of the `registryvocab` leaf, which declares
  the string fields ecosystem names travel in, and the leaf itself). It
  also covers `internal/credresolver`, which never imports the ecosystem
  package.

## Amendment (2026-10-18 architecture review): credential stores stay one table; rows name the stores they read

The decision above put each ecosystem's credential store on its row. The
code never followed, and the review agreed it should not:

- A store is not one ecosystem's. `npmrc` serves npm, yarn and pnpm.
- The seven credential sources form the routes file's own grammar. Their
  order is visible to operators in its errors and in its retired-key
  renderer.

A parser on npm's row that yarn and pnpm borrow would be the cross-row
dependency this ADR rules out.

Credential sources therefore stay one table in `credresolver`. Each
Ecosystem row declares, by source key, which stores it reads, and
`registrydiscover.DiscoverRows` derives that map from the rows it is
given with `ecosystem.CredentialStores(rows)`, as the proxy's rewrite
rows come from the same table. That removes
`f.Ecosystem == "cargo"`, the one name literal the containment scan
could not reach.

The same review added two things:

- **Ecosystem corpora.** Each row's corpus is replayed by an
  `ecosystemtest` kit through lockfile classification, the lockfile scan,
  discovery, route resolution, path-set derivation, binding, the bindings
  summary and proxy rewrites, with a golden at each stage.
  Consumer packages test their generic behaviour against a synthetic row
  and stop naming real ecosystems.
- **No per-row literals.** Env-export order derives from table position.
  The reference prose about each ecosystem renders from the rows.

File-per-ecosystem stands. None of the promotion triggers above has fired.

*Amended by issue #5741:* the remaining launcher main-package and Box
tests fall in or out of the synthetic-row move as follows.

- **Registry-route drift check.** The doctor check takes its rows as a
  parameter, and production passes `ecosystem.Table`. Its tests run on
  the synthetic row. A real row's multi-registry parsing, such as cargo
  declaring two registries on one host, stays pinned by its corpus
  (`cargo/replaced-source-two-registries`).
- **Route resolution.** `registryroutesresolve_test.go` had already
  moved (commit 48825cae).
- **Credential-source tests.** `checks_test.go` and `bootstrap_test.go`
  stay as they are. They name real stores only as credential-source
  keys, which are the routes file's own grammar (above), not row
  knowledge, so a synthetic row has nothing to replace them with.
  Neither writes an in-tree registry config or a lockfile.
- **Box seam golden.** `box/box_seam_test.go` keeps asserting over
  `ecosystem.Table`. It builds and runs the shipped `box` binary, which
  binds the real table. A row seam there would need a test-only switch
  compiled into that binary. The Box's generic registry behaviour is
  already tested in-process on the synthetic row
  (`box/registry_test.go`), so the golden stays the one integration
  smoke over the production table.

## Amendment (2026-10-22 registry kit review): an ecosystem is a package behind a conformance kit

The decision above rejected one Go package per ecosystem. The
ecosystems never call each other, so the seam would contain nothing,
and registration order would hide the precedence order. Two things
have changed since:

- The 2026-10-18 amendment gave every row a corpus that `ecosystemtest`
  replays through all six pipeline stages. The Go test check is one
  derivation over the whole package, so an edit to yarn reruns cargo's
  corpus. Drivers had the same problem, and ADR 0066 decision 5 solved
  it with one test derivation per package. Ecosystems now carry enough
  per-row tests to need the same fix.
- A seventh ecosystem, NuGet, is planned. It is the first one added
  since the corpora landed, so it should only add a new package and its
  corpus, and change nothing that already exists.

Neither trigger above fired. This amendment replaces the "Considered
option" instead.

1. **One package per ecosystem, the driverkit shape.** Each ecosystem
   moves under `internal/ecosystem/<name>` and exports a Strategy. The
   Strategy is today's Row, renamed, with its nil-able hook fields
   unchanged. `internal/ecosystem` keeps the type, the registry and the
   accessors (`EnvExportRowsOf`, `CredentialStores`, and so on). The
   packages are `cargo`, `npmfamily`, `gomod` and `gradle`. `go` is a Go
   keyword, so the go ecosystem's package is `gomod`, but its Strategy is
   still named `"go"`.
2. **The npm family is one package with three Strategies.** npm, yarn
   and pnpm share `npmFamilyBinding`, the YAML scalar helpers and the
   tarball rewrite. In one package that sharing is internal, not a
   cross-row dependency. ADR 0069's runner families set the precedent.
3. **The kit holds only what no ecosystem owns.** A new
   `internal/ecosystem/ecosystemkit` takes the response-rewrite helpers
   with no ecosystem in them (`RepointRegistryURL`,
   `DecodeOneJSONObject`, `normalizeHostPort`), `HTTPAbsoluteURL`, and
   the route helper `DeclaredPath`.
   Anything that names an ecosystem's file format stays in that
   ecosystem's package. `ecosystemtest` stays the conformance kit. The
   synthetic row becomes a synthetic Strategy.
4. **Precedence is one ordered list in the Bundle.** A new
   `internal/driver/bundle/ecosystems.go` makes one call,
   `ecosystem.Register(cargo.Strategy, npmfamily.Npm, npmfamily.Yarn,
   npmfamily.Pnpm, gomod.Strategy, gradle.Strategy)`, with the order in
   the argument list. A second call panics. Lockfile precedence and the
   env-export line order are read from that one visible list, not from
   import or `init` order, which answers the objection above. New
   ecosystems append to the end unless their lockfiles collide with an
   existing row's.
5. **No Nix row.** Unlike a Driver's or a backend's facts, no ecosystem
   fact crosses into Nix: classification, the reference docs (#5663) and
   the export order (#5662) are all read in Go. The per-ecosystem test
   derivations map over `builtins.readDir` of `internal/ecosystem` (every
   directory but the kit and `testdata`), so
   no ecosystem is named in Nix by hand. *Reopen trigger:* the first
   ecosystem fact the image has to bake in, such as a Box-side package
   one ecosystem needs. That fact becomes a Nix row rendered into a
   generated Descriptor, the way backends do it.
6. **Two stages.**
   - **Stage 1:** the moves, the kit, the Bundle's `ecosystems.go`, and the
     per-package derivations. `ecosystem.Table` becomes the registered
     list. The `Table`-default wrappers in `registrypathset`,
     `bindregistry`, `registryroutes` and `registrydiscover` read the
     registry, so no caller changes.
   - **Stage 2:** delete those wrappers. The five binary call sites
     (`main.go`, `bootstrap.go`, `box/registry.go`, `driver-exec` and
     `dispatch/box.go`) pass the registered Strategies, so libraries
     only take rows as arguments.

   Since #5653 gave every stage a `...Rows` variant, stage 2 only deletes
   code. A new ecosystem waits for stage 1 only.

Consequences:

- The containment scan (#5661) now reads: only an ecosystem's own
  package and the Bundle's `ecosystems.go` name that ecosystem.
- The orchestrator imports no ecosystem today and still imports none.
  `ecosystems.go` is left out of its fileset, as `backends.go` is
  (issue #5738), and kept in the Box's and `driver-exec`'s, since both
  bind registries.
- Adding an ecosystem takes a package, its corpus, and an import plus one
  `Register` argument in `ecosystems.go`.
- Upstream host products are a separate axis, covered by ADR 0071, not
  by any ecosystem.
- Stage 1 closed in issue #5958. The Ecosystem row is now the Ecosystem
  Strategy throughout, the glossary names its package and its corpus
  home, and the core `internal/ecosystem` package holds no ecosystem's
  file-format code.
