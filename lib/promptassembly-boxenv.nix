# The promptassembly.Env box-env accessor row list (issue #2979): the 29
# Env fields (cmd/launcher/internal/promptassembly/env.go) that
# driver-exec/assembleprompt_cmd.go now populates via EnvFromEnviron reading
# a real Box OS-process env var directly -- previously a hand-declared CLI
# flag, itself forwarded 1:1 by agent/entrypoint.sh's phase_prompt_assembly,
# until this issue dropped those flags. Each row's env var is either
# forwarded by the Go launcher's dispatch code
# (cmd/launcher/internal/dispatch/dispatch.go's buildBoxEnv, box.go) or via
# lib/env-schema.nix's boxEnv = true knob-forwarding mechanism. This list is
# the source lib/renderers.nix's renderPromptAssemblyBoxEnvGo renders into
# cmd/launcher/internal/promptassembly/boxenv_gen.go's EnvFromEnviron, which
# reads each row's env var directly via os.Getenv -- the same
# small-Nix-list -> pure renderer -> nix/regen.nix -> drift-check house
# style lib/baked-skills.nix and lib/env-schema.nix's schemaConfig family
# (cmd/launcher/schemaconfig_gen.go) already established.
#
# Deliberately NOT part of lib/env-schema.nix: these 29 rows are not
# operator-facing knobs. Most are per-dispatch facts (ISSUE_NUMBER,
# DISPATCH_KIND, ...) or nix-precomputed static gate values
# (BOX_TRACKER_AXIS_READ, BOX_FORGE_BACKEND, ...) the launcher forwards into
# the Box, not settings an operator's flake `settings` block sets.
#
# Out of scope for this list, and so still CLI flags on
# assembleprompt_cmd.go:
#   - The 6 *SkillBaked bool fields + SkillsFound: filesystem probes
#     entrypoint.sh resolves by statting DRIVER_SKILLS_DIR before invoking
#     assemble-prompt, not env reads (lib/baked-skills.nix already owns the
#     *SkillBaked family).
#   - PromptsDir, DriverAgentFilesDir, CommsContractFile,
#     CheckContractFile, CodeCommentsContractFile, OutcomeContractFile,
#     ResearchOutcomeContractFile: path-shaped CLI inputs, kept as flags per
#     issue #2979's "Flags survive only for non-env inputs: skills-probe
#     results and output paths."
#   - AgentsPromptFiles: a nix-baked agent-name -> promptFile JSON map, not
#     a path itself, but kept as a flag for a different reason -- lib/image.nix
#     assigns its value to the plain (unexported) bash local
#     AGENTS_PROMPT_FILES, unlike its sibling AGENTS_JSON_TEMPLATE which this
#     issue's entrypoint.sh changes now `export`. A separate compiled
#     process such as driver-exec can't see an unexported bash local via
#     os.Getenv, so this field has no env var to read until that export is
#     added.
#
# Each row:
#   field - the promptassembly.Env struct field name this row populates.
#   env   - the Box OS-process env var name EnvFromEnviron reads via
#           os.Getenv.
#   kind  - which read rule the renderer emits for this row:
#             presence - os.Getenv(env) != ""
#             string   - os.Getenv(env) verbatim
#             int      - strconv.Atoi(os.Getenv(env)), degrading to 0 on
#                         empty/malformed input. Unlike
#                         cmd/launcher/main.go's atoiSchema, which falls back
#                         to a per-key schema default (intSchemaDefault),
#                         these 29 rows are deliberately outside
#                         lib/env-schema.nix (see above) and so have no
#                         schema default to degrade to.
#             equals1  - os.Getenv(env) == "1"
[
  {
    field = "OrchestratorEnabled";
    env = "ORCHESTRATOR_ENABLED";
    kind = "presence";
  }
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
    field = "ReviewLoopInline";
    env = "BOX_REVIEW_LOOP_INLINE";
    kind = "presence";
  }
  {
    field = "ReviewLoopOrchestrator";
    env = "BOX_REVIEW_LOOP_ORCHESTRATOR";
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
    field = "ResearchStatusEnum";
    env = "RESEARCH_STATUS_ENUM";
    kind = "string";
  }
]
