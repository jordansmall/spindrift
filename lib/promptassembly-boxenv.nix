# Source rows for lib/renderers.nix's renderPromptAssemblyBoxEnvGo, which
# renders promptassembly/boxenv_gen.go's EnvFromEnviron (issue #2979). kind
# picks the read rule: presence (non-empty), string, equals1, or int, which
# degrades to 0 on empty or malformed input because these rows have no
# env-schema default to fall back on.

# These rows stay out of lib/env-schema.nix on purpose: they are per-dispatch
# facts and nix-precomputed gate values the launcher forwards into the Box,
# not knobs an operator sets. AgentsPromptFiles has no row because lib/image.nix
# keeps AGENTS_PROMPT_FILES an unexported bash local, so os.Getenv in a
# separate process cannot see it.

# Fields that are not env reads stay CLI flags on assembleprompt_cmd.go per
# issue #2979: the *SkillBaked probes entrypoint.sh resolves by statting
# DRIVER_SKILLS_DIR, and the output path inputs.
[
  {
    field = "AgentsJSONTemplate";
    env = "AGENTS_JSON_TEMPLATE";
    kind = "string";
  }
  {
    field = "FilerEnabled";
    env = "BOX_FILER_ENABLED";
    kind = "presence";
  }
  {
    field = "WorkerProvisioned";
    env = "BOX_WORKER_PROVISIONED";
    kind = "presence";
  }
  {
    field = "ScoutProvisioned";
    env = "BOX_SCOUT_PROVISIONED";
    kind = "presence";
  }
  {
    field = "IssueTracker";
    env = "ISSUE_TRACKER";
    kind = "string";
  }
  {
    field = "TrackerAxisRead";
    env = "BOX_TRACKER_AXIS_READ";
    kind = "string";
  }
  {
    field = "TrackerAxisWrite";
    env = "BOX_TRACKER_AXIS_WRITE";
    kind = "string";
  }
  {
    field = "TrackerAxisFiler";
    env = "BOX_TRACKER_AXIS_FILER";
    kind = "string";
  }
  {
    field = "BoxWriteEnabled";
    env = "BOX_WRITE_ENABLED";
    kind = "presence";
  }
  {
    field = "LocalIssueReference";
    env = "LOCAL_ISSUE_REFERENCE";
    kind = "presence";
  }
  {
    field = "CodeForge";
    env = "CODE_FORGE";
    kind = "string";
  }
  {
    field = "ForgeBackend";
    env = "BOX_FORGE_BACKEND";
    kind = "string";
  }
  {
    field = "DispatchKind";
    env = "DISPATCH_KIND";
    kind = "string";
  }
  {
    field = "DispatchKey";
    env = "DISPATCH_KEY";
    kind = "string";
  }
  {
    field = "DispatchKeying";
    env = "DISPATCH_KEYING";
    kind = "string";
  }
  {
    field = "DispatchAnnounceVerb";
    env = "DISPATCH_ANNOUNCE_VERB";
    kind = "string";
  }
  {
    field = "SignalCarrier";
    env = "BOX_SIGNAL_CARRIER";
    kind = "string";
  }
  {
    field = "SelfContained";
    env = "SELF_CONTAINED";
    kind = "equals1";
  }
  {
    field = "FixPass";
    env = "FIX_PASS";
    kind = "int";
  }
  {
    field = "ResumeAfterHold";
    env = "RESUME_AFTER_HOLD";
    kind = "presence";
  }
  {
    field = "AutoFormat";
    env = "AUTO_FORMAT";
    kind = "presence";
  }
  {
    field = "AutoLint";
    env = "AUTO_LINT";
    kind = "presence";
  }
  {
    field = "CIFailureSummary";
    env = "CI_FAILURE_SUMMARY";
    kind = "string";
  }
  {
    field = "IssueNumber";
    env = "ISSUE_NUMBER";
    kind = "string";
  }
  {
    field = "IssueTitle";
    env = "ISSUE_TITLE";
    kind = "string";
  }
  {
    field = "IssueText";
    env = "ISSUE_TEXT";
    kind = "string";
  }
  {
    field = "Branch";
    env = "BRANCH";
    kind = "string";
  }
  {
    field = "BaseBranch";
    env = "BASE_BRANCH";
    kind = "string";
  }
  {
    field = "InProgressLabel";
    env = "IN_PROGRESS_LABEL";
    kind = "string";
  }
  {
    field = "CompleteLabel";
    env = "COMPLETE_LABEL";
    kind = "string";
  }
  {
    field = "RunNonce";
    env = "RUN_NONCE";
    kind = "string";
  }
  {
    field = "RecordID";
    env = "RECORD_ID";
    kind = "string";
  }
  {
    field = "ResearchVerdicts";
    env = "RESEARCH_VERDICTS";
    kind = "string";
  }
  {
    field = "ReviewModelOverride";
    env = "BOX_REVIEW_MODEL_OVERRIDE";
    kind = "string";
  }
  {
    field = "ReviewEffortOverride";
    env = "BOX_REVIEW_EFFORT_OVERRIDE";
    kind = "string";
  }
  {
    field = "ChoreName";
    env = "CHORE_NAME";
    kind = "string";
  }
  {
    field = "ChoreHead";
    env = "CHORE_HEAD";
    kind = "string";
  }
  {
    field = "ChoreDiffRange";
    env = "CHORE_DIFF_RANGE";
    kind = "string";
  }
  {
    field = "ChoreSlice";
    env = "CHORE_SLICE";
    kind = "string";
  }
  {
    field = "ChoreClasses";
    env = "CHORE_CLASSES";
    kind = "string";
  }
  {
    field = "ChoreClassList";
    env = "CHORE_CLASS_LIST";
    kind = "string";
  }
  {
    field = "ChorePatchClasses";
    env = "CHORE_PATCH_CLASSES";
    kind = "string";
  }
  {
    field = "ChoreMaxFindings";
    env = "CHORE_MAX_FINDINGS";
    kind = "string";
  }
  {
    field = "HostMediatedRemote";
    env = "BOX_HOST_MEDIATED_REMOTE";
    kind = "presence";
  }
  {
    field = "OutboxRelayCapable";
    env = "BOX_OUTBOX_RELAY_CAPABLE";
    kind = "presence";
  }
]
