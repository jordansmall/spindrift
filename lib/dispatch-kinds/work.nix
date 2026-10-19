{
  name = "work";
  verb = "dispatch";
  keying = "ByIssue";
  labels = "LabelsConfigured";
  settle = "SettleMerge";
  daemonPriority = "PriorityNormal";
  enablement = "EnabledAlways";
  demandSource = "DemandTrackerProbe";
  findingLabel = "agent-review-finding";
  contract = "ContractLanding";
  filerRelayGate = "FILER_FILE_RELAY_WORK";
  tracker = "TrackerWork";
  announceVerb = "implementing";
  preflight = {
    when = "none";
  };
  statuses = [
    "ready"
    "blocked"
    "ambiguous"
    "already-resolved"
  ];
  goVar = "Work";
  default = true;
}
