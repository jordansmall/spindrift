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
row is in it. Consumers walk the table; none spells a name. Alongside it,
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
  package plus the launcher's main package.
