# Queue-mode `spindrift recover` (issue #4656): it relays a finished work
# Box's stranded outbox bundle through the work merge gate and runs no Box of
# its own, so its Box-facing fields mirror work's.
# PriorityFirst lands stranded work before a new Box starts, so each rebases
# onto as little new main as possible. Its work is outbox bundles, not
# tracker issues, so the host counts the eligible ones itself
# (DemandHostOutbox), leaving out bundles a running daemon child holds; the
# count is an upper bound, so a child may still exit 2 (nothing eligible).
{
  name = "recover";
  verb = "recover";
  keying = "ByIssue";
  labels = "LabelsConfigured";
  settle = "SettleMerge";
  daemonPriority = "PriorityFirst";
  enablement = "EnabledByOutboxRelay";
  demandSource = "DemandHostOutbox";
  findingLabel = "agent-review-finding";
  contract = "ContractLanding";
  filerRelayGate = "FILER_FILE_RELAY_WORK";
  tracker = "TrackerWork";
  announceVerb = "recovering";
  preflight = {
    when = "none";
  };
  goVar = "Recover";
}
