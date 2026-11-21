# The one-shot butler run (ADR 0056, #3870): it carries one Ledger Chore,
# never a tracker issue, and files findings advise-only like research. Its
# only doctor-visible label is the agent-butler-finding provenance one.
# PriorityIdle: a slot picks it only once dispatch and research both report
# no work, and a running butler child is never preempted.
rec {
  name = "butler";
  verb = "butler";
  keying = "ByChore";
  labels = "LabelsNone";
  prompts = {
    base = "butler-prompt.md";
    reviewer = "butler-review-prompt.md";
  };
  settle = "SettleLedger";
  adviseOnly = true;
  completeBanner = "findings filed";
  daemonPriority = "PriorityIdle";
  enablement = "EnabledByChores";
  demandSource = "DemandChildReported";
  findingLabel = "agent-butler-finding";
  patchLabel = "agent-butler-patch";
  contract = "ContractInline";
  filerRelayGate = "FILER_FILE_RELAY_BUTLER";
  # Butler files findings onto the work tracker.
  tracker = "TrackerWork";
  announceVerb = "sweeping";
  unclaimedGate = true;
  preflight = {
    when = "drawn";
    flag = "--butler";
  };
  statuses = (import ../prompt-contract.nix).outcomeStatusesFor name;
  goVar = "Butler";
}
