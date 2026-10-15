---
title: "fix(bindregistry): bound in-tree registry rewrites to a host boundary"
state: ready-for-agent
---
## Problem

`ApplyInTreeBinding` (`cmd/launcher/internal/bindregistry/intreebinding.go:285-290`) rewrites every occurrence of `"https://" + UpstreamHost` and `"http://" + UpstreamHost` in a tracked `.npmrc`, `.yarnrc.yml`, or `pnpm-workspace.yaml` using `replaceAllFold` (`intreebinding.go:141-157`). That is a plain substring match with no host boundary check: the byte after the matched host is never inspected, so any URL whose host merely *starts with* a route's upstream host is rewritten too.

The presence check that gates the rewrite (`intreebinding.go:246-252`, `strings.Contains(foldedContent, "https://"+host)`) has the same gap, so a config naming only such a look-alike URL also gets rewritten and tagged skip-worktree.

The code already knows host-prefix overlap is a hazard: the longest-host-first ordering at `intreebinding.go:272-279` exists precisely because `registry.example.com` would otherwise match inside `registry.example.com:8443`. But that ordering only helps when *both* hosts are configured routes. When only the shorter host is a route, the longer, unrelated URL is still corrupted.

## Reproduction

With a single rewrite `{UpstreamHost: "nexus.corp", LocalURL: "http://127.0.0.1:27182/r0"}`, `replaceAllFold` turns:

```
@a:registry=https://nexus.corp:8081/repository/npm/
@b:registry=https://nexus.corp.mirror.net/npm/
```

into:

```
@a:registry=http://127.0.0.1:27182/r0:8081/repository/npm/
@b:registry=http://127.0.0.1:27182/r0.mirror.net/npm/
```

(Verified by calling `replaceAllFold` directly in a throwaway package test.) Both scoped registries now point at malformed Forwarder paths, so installs for those scopes fail inside the Box. A same-host-different-port setup (for example a Nexus serving npm on 443 and another repository on 8081) is the realistic trigger. `FoldHost`'s own doc says `registry.example.com` and `registry.example.com:8443` are meant to be *distinct* rewrites.

## Suggested fix

Only accept a match when the byte following the matched `scheme://host` is a URL host terminator (`/`, `?`, `#`, a quote, whitespace, or end of input), or `:` only when the configured `UpstreamHost` itself carries no port and the following text is not a port. Apply the same boundary rule to the `anyHostPresent` check so the two can't disagree. Add a test with a single portless route alongside a config naming the same host on another port and a longer host with that prefix, asserting both are left untouched.


Filed by the butler's `bugs` Chore. Class: `correctness`. Files: `cmd/launcher/internal/bindregistry/intreebinding.go`.

<!-- spindrift-dedup: cmd:launcher:internal:bindregistry:intreebinding:go:replaceallfold, cmd:launcher:internal:bindregistry:intreebinding:go:applyintreebinding -->
