rec {
  name = "work";
  verb = "dispatch";
  keying = "ByIssue";
  labels = "LabelsConfigured";
  prompts = {
    issue = "issue-prompt.md";
    fix = "fix-prompt.md";
  };
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
  statuses = (import ../prompt-contract.nix).outcomeStatusesFor name;
  goVar = "Work";
  default = true;
}
