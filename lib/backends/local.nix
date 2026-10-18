# The local backend row: pure data only (issue #5633). The registry
# (./default.nix) documents the fields and fixes this row's order.
{
  name = "local";
  goVar = "Local";
  validAsTracker = true;
  validAsCodeForge = true;
  # ADR 0033: local has no writable remote to push to.
  hostMediatedRemote = true;
  # ADR 0032: local is not reachable from inside the Box at all.
  inBoxUnreachableTracker = true;
  relayCapable = true;
  hostPostingCapable = true;
  trackerAxisRead = "LOCAL";
  # Explicitly empty, not omitted: local has no write step, so
  # gates_tracker.go's ISSUE_TRACKER_*_READWRITE/READONLY gates match neither
  # the "GITHUB" nor the "FORGEJO" arm.
  trackerAxisWrite = "";
}
