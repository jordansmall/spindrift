# Local issues are the one host-mediated tracker: read-only mount in, launcher-posted comments out

## Context

The Box clones the Target repo fresh from a git *remote* and shares no host
filesystem (CONTEXT.md: "never read from a host checkout"; entrypoint.sh:
"zero shared host filesystem"). Its only issue-tracker client is per-remote:
the prompts read an issue with `gh issue view ${ISSUE_NUMBER} --comments` and
write with `gh issue comment ${ISSUE_NUMBER}`, and the Launcher forwards only
`ISSUE_NUMBER`/`ISSUE_TITLE` into the Box — never the body.

That is fine for a remote tracker but breaks entirely for `local` (ADR 0013,
0029). A local issue is a git-ignored Markdown file under `LOCAL_ISSUES_DIR`
(default `.spindrift/issues/`), kept private on purpose. It is therefore **not
in the fresh clone**, there is **no server** to reach, and `gh issue view N`
inside the Box either fails or — worse, for a numeric slug like `42.md` —
silently fetches the wrong real issue #42 on `REPO_SLUG` (the same collision
ADR 0029 flagged for `Closes #42`). So a `local` Dispatch works blind, and a
`local` research Dispatch cannot post the verdict comment that is its entire
deliverable.

The lifecycle plane is not affected: every Dispatch-state transition
(`TransitionState`, `RecordLanding`, `CloseIssue`, `reconcile`, `settle`)
already runs host-side against the `LocalTracker`, for every tracker. The gap
is the **content plane** — reading an issue's body/comments and writing a
comment back — which was wired to the GitHub remote with no tracker-agnostic
path.

## Decision

**Split trackers by in-box reachability, and make `local` the single
host-mediated exception.**

- **Remote trackers** (`github` today; `gitlab`/`bitbucket`/`jira` as they
  land) are reachable from inside the Box over the network via their own
  client, so the Box **reads and writes them in-box**, unchanged. github keeps
  `gh issue view --comments` (live body + comments + linked issues) and
  `gh issue comment`.

- **`local`** has no in-box reachability, so its content plane is
  **host-mediated**:
  - **Read — a read-only mount.** The Launcher RO bind-mounts
    `LOCAL_ISSUES_DIR` into the Box at the fixed top-level target `/issues`;
    the agent reads `/issues/${ISSUE_NUMBER}.md` and follows its `## Blocked
    by`/`parent` links straight from the folder — the same linked-issue
    enrichment `gh issue view` gives a remote tracker, transitively and for
    free. The mount is the **single, named exception** to the Box's
    zero-shared-host-filesystem rule.
  - **Write — the Box never touches the tracker.** Read-only structurally
    enforces it. The Box emits its comment as a delimited block on stdout
    (`SPINDRIFT_COMMENT_BEGIN … SPINDRIFT_COMMENT_END`); the Launcher extracts
    the last complete block from the Box log (the channel `LastInLog` already
    scans for `SPINDRIFT_OUTCOME`) and posts it host-side via
    `LocalTracker.Comment`. Research's verdict rides this block; work's
    blocked-note reuses the outcome `note=`. For `local` research, `landing`
    is `none` (there is no comment URL — the Launcher did the posting) and the
    verdict rides `status=`.

The Box already branches on `ISSUE_TRACKER` in-box (issue #1429, the PR-body
fragment); the read and comment steps in the prompts gain the same
github-vs-`local` branch.

## bwrap is the least-capable runner and sets the ceiling

The two runners (ADR 0006) are not equally capable, and the weaker one —
bubblewrap — constrains the design:

- **Mount target is `/issues`, not `/agent/issues`.** bwrap binds `/agent` as
  a read-only nix store path and cannot fabricate a nested mountpoint inside
  it; `/agent/prompts` works only because it is baked into the store dir. A
  top-level `/issues` is auto-created in bwrap's own root (a read-only bind
  target needs no writable-mount `--dir` dance, unlike the driver cache, issue
  #427). OCI auto-creates `-v` targets either way.
- **Writes must be "Box emits, Launcher writes."** bwrap's root is a tmpfs on
  an ephemeral child process that vanishes on exit, so there is no
  copy-results-out step — the only way to get bytes out of bwrap is a
  *writable* host bind, a larger breach than the read-only read mount. The
  stdout comment block needs no such bind.

## Security

The read-only mount clears the isolation bar:

- **No new escape primitive.** Read-only is enforced by the runtime (bwrap
  `--ro-bind` under `--unshare-user --uid 1000`; OCI `:ro` atop
  `--cap-drop=all --security-opt=no-new-privileges`), the same mechanism as
  the existing `/nix/store` and `/agent` binds. The Box cannot write the
  operator's issues — which is exactly the "Launcher owns writes" property we
  want.
- **Symlink resolution is contained** to the Box's sparse mount namespace
  (nix store, agent files, synthesized `/etc`, tmpfs) — a symlink in the
  issues dir pointing at a host secret resolves to a dead path, *safer* than a
  host-side forward read where `os.ReadFile` would follow it into the host
  filesystem.
- **Whole-folder readability is consistent** with the ambient `gh` access a
  github Box already has (it can `gh issue list`/`view` any issue in the
  Target tracker), so a `local` Box seeing sibling issues is not a new
  exposure class — and for a solo operator they are the operator's own private
  files.

## Considered Options

- **Forward the body (and a 1-hop linked-issue bundle) via an env var the
  entrypoint writes to a file** — viable and preserves the zero-shared-fs
  invariant with strict least-privilege, but costs the bundle-gathering
  plumbing and gives no linked-issue enrichment beyond one hop. Rejected in
  favor of the mount's simpler host-side story and free transitive
  enrichment; the security edge did not survive scrutiny (see above).
- **A read-write mount** — lets the Box write the local file directly, but
  makes the Box a second writer alongside the Launcher's structured
  `parse→render` rewrite (`reconcile`, `CloseIssue`), risking lost updates,
  and couples the Box to the frontmatter/body layout. Rejected: read-only +
  Launcher-posted keeps a single writer.
- **Pull *all* tracker writes out of the agent, uniformly (Launcher posts for
  every tracker)** — tempting for a single write path, but a remote tracker
  keeps its client in-box for *reads* regardless, so centralizing its writes
  removes no in-box authority and only churns github's proven
  `gh issue comment` path. Rejected: only `local` needs host-mediated writes.
- **Bake tickets into the image via nix** — a per-issue image rebuild on every
  ticket edit (the freshness probe, #526, would flap stale), puts private
  content into a distributable, persistent artifact, and is a nix impurity.
  Rejected: per-issue *mutable* data never rides the nix-built image.
- **Copy tickets in and pull results back out (`podman cp`)** — the copy-in
  half is just the forwarded bundle with more machinery; the copy-out half is
  impossible on bwrap (ephemeral tmpfs) without a writable mount, and even on
  OCI needs a field-level merge of the Box's divergent copy against the
  Launcher's host-side writes. Rejected.

## Consequences

- The prompts (`issue-prompt.md`, `research-prompt.md`, `scout`/`review`)
  gain a github-vs-`local` branch on the issue-read and comment steps; the
  Box reads `/issues/${ISSUE_NUMBER}.md` and emits the comment block under
  `local`.
- A new `SPINDRIFT_COMMENT_BEGIN … SPINDRIFT_COMMENT_END` grammar and a
  last-wins log extractor join `outcome`; `settle` wires the extracted
  comment (research verdict) and the outcome `note=` (work blocked-note)
  to a host-side `LocalTracker.Comment`, and a missing/malformed block is
  treated as blocked.
- The `local` mount is added through `buildMountSpecs` (the Launcher computes
  the spec; the runners stay tracker-agnostic), gated on
  `ISSUE_TRACKER=local` and `candidateMount` (the issues dir must exist at
  dispatch — it does when there is an issue to dispatch).
- The zero-shared-host-filesystem claim now carries one documented exception:
  a read-only mount of `LOCAL_ISSUES_DIR` under `ISSUE_TRACKER=local`.
- `gitlab`/`bitbucket`/`jira` join the remote path (in-box client, read and
  write) as their adapters land; none needs the mount or the comment block.
