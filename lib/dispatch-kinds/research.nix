rec {
  name = "research";
  verb = "research";
  keying = "ByIssue";
  labels = "LabelsResearch";
  prompts = {
    base = "research-prompt.md";
    selfContainedBase = "research-self-contained-prompt.md";
  };
  settle = "SettleVerdict";
  adviseOnly = true;
  completeBanner = "verdicts posted";
  daemonPriority = "PriorityReserved";
  enablement = "EnabledAlways";
  demandSource = "DemandTrackerProbe";
  findingLabel = "agent-research-finding";
  contract = "ContractVerdict";
  filerRelayGate = "FILER_FILE_RELAY_RESEARCH";
  tracker = "TrackerResearch";
  announceVerb = "researching";
  preflight = {
    when = "named";
    flag = "--research";
  };
  statuses = (import ../prompt-contract.nix).outcomeStatusesFor name;
  goVar = "Research";
}
