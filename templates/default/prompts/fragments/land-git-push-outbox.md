1. This combination — `BOX_FORGE_AND_ISSUE_ACCESS=read-only` with
   `CODE_FORGE=git` — is never dispatched: the launcher's startup capability
   gate refuses it, since CODE_FORGE=git implements no bundle-relay
   mechanism for a host-mediated push. If you somehow reach this branch
   anyway, treat it as a launcher misconfiguration and follow IF BLOCKED
   rather than attempting to push directly.
