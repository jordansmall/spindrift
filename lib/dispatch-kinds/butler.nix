# The one-shot butler run (ADR 0056, #3870): it carries one Ledger Chore
# (ByChore), never a tracker issue, and files findings the same advise-only
# way research does, so it never lands code either. It has no lifecycle
# labels of its own (LabelsNone) — its only doctor-visible label is the
# agent-butler-finding provenance one. Its PriorityIdle means a slot picks it
# only once dispatch and research have both reported no work (the
# awake-window rule: gate starting, never stopping — a running butler child
# is never preempted once picked).
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
  # issue #3906
  readOnlyBox = true;
  daemonPriority = "PriorityIdle";
  enablement = "EnabledByChores";
  demandSource = "DemandChildReported";
  findingLabel = "agent-butler-finding";
  patchLabel = "agent-butler-patch";
  contract = "ContractInline";
  filerRelayGate = "FILER_FILE_RELAY_BUTLER";
  # Butler files findings onto the work tracker; it has no lifecycle labels
  # of its own.
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
