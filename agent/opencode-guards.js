// opencode plugin (issue #5346). The claude PreToolUse hooks never run under
// opencode, so its bash and read tools would otherwise keep the Box's
// credentials, accept backgrounded work and reach credential files. This runs
// the very same baked hooks on each call, so the guard logic, its messages, and
// the credential list (credential-vars) stay single-sourced rather than
// re-implemented here. A throw here precedes opencode's permission check, so no
// Target-repo opencode.json allow rule can widen a refusal.
// Plain ESM with node: builtins only: opencode's background `npm install` into
// this directory fails offline, so nothing here may import a package.
import { execFileSync } from "node:child_process";

// Baked for every Driver by lib/image.nix's homeLayoutScript.
const HOOKS_DIR = "/home/agent/.claude/hooks";
const GUARD_TIMEOUT_MS = 10_000;

// Generated from lib/bash-hooks.nix, which documents the run order, `tools`
// and `rewrites`.
// BEGIN GENERATED OPENCODE HOOKS -- nix run .#regen -- DO NOT EDIT
const HOOKS = [
  { script: "reject-background-bash.sh", tools: ["bash"], rewrites: false },
  { script: "credential-deny.sh", tools: ["read", "bash"], rewrites: false },
  { script: "env-credential-scrub.sh", tools: ["bash"], rewrites: true },
  { script: "bash-output-tee.sh", tools: ["bash"], rewrites: true },
];
const SUMMARY_HOOK = "bash-output-summary.sh";
// END GENERATED OPENCODE HOOKS
// opencode appends a block opened by this and closed by SHELL_METADATA_CLOSE to
// a result it cut short (timeout, user abort); the summary replaces the whole
// output, so the notice is carried over.
const SHELL_METADATA_OPEN = "\n\n<shell_metadata>\n";
const SHELL_METADATA_CLOSE = "</shell_metadata>";

// The hooks read claude's tool names and argument names; opencode's read tool
// calls its path `filePath`, where claude's calls it `file_path`.
const CLAUDE_TOOLS = {
  bash: { name: "Bash", input: (args) => args },
  read: { name: "Read", input: (args) => ({ file_path: args.filePath }) },
};

// Every tool a generated hook names must be mapped: the handler skips unmapped
// tools, which would leave one silently unguarded.
for (const { script, tools } of HOOKS) {
  for (const tool of tools) {
    if (!Object.hasOwn(CLAUDE_TOOLS, tool)) {
      throw new Error(`spindrift-guards: ${script} names tool ${tool} with no CLAUDE_TOOLS entry`);
    }
  }
}

// A guard without a working jq exits 0 and allows the call (see guardHook in
// lib/image.nix), so a silent exit 0 is only trustworthy once jq has run.
let jqWorks = false;
function requireJq() {
  if (jqWorks) return;
  try {
    execFileSync("jq", ["-n", "true"], { stdio: "ignore", timeout: GUARD_TIMEOUT_MS });
  } catch (err) {
    throw new Error(`guard hooks cannot run (no working jq): ${err.message}, refusing the call`);
  }
  jqWorks = true;
}

function runHook(script, toolName, args, onFailure = "") {
  const tool = CLAUDE_TOOLS[toolName];
  const payload = JSON.stringify({ tool_name: tool.name, tool_input: tool.input(args) });
  let out;
  let parsed;
  try {
    out = execFileSync(`${HOOKS_DIR}/${script}`, [], {
      input: payload,
      encoding: "utf8",
      timeout: GUARD_TIMEOUT_MS,
    });
    parsed = out.trim() === "" ? {} : JSON.parse(out);
  } catch (err) {
    throw new Error(`${tool.name} hook ${script} failed: ${err.message}${onFailure}`);
  }
  return parsed.hookSpecificOutput ?? {};
}

export const SpindriftGuards = async () => ({
  "tool.execute.before": async (input, output) => {
    if (!Object.hasOwn(CLAUDE_TOOLS, input.tool)) return;
    requireJq();
    for (const { script, tools, rewrites } of HOOKS) {
      if (!tools.includes(input.tool)) continue;
      // Fail closed: a hook that cannot run or answer must not wave the call
      // through with its credentials intact or without the log the tee keeps.
      const decision = runHook(script, input.tool, output.args, ", refusing the call");
      if (decision.permissionDecision === "deny") {
        throw new Error(decision.permissionDecisionReason ?? `Guard ${script} denied the call`);
      }
      // Mutate in place: opencode executes the tool with this same args object.
      if (typeof decision.updatedInput?.command === "string") {
        output.args.command = decision.updatedInput.command;
      } else if (rewrites && typeof output.args.command === "string" && output.args.command !== "") {
        // The rewriting hooks exit silently for an empty command only.
        throw new Error(`Bash guard ${script} returned no rewrite, refusing the call`);
      }
    }
  },
  // The summary hook owns the output bound (BASH_OUTPUT_SUMMARY_TAIL_BYTES); a
  // short result passes through untouched. input.args is the object the before
  // hook rewrote, so it still carries the tee wrapper the summary parses.
  "tool.execute.after": async (input, output) => {
    if (input.tool !== "bash") return;
    try {
      const updated = runHook(SUMMARY_HOOK, input.tool, input.args).updatedToolOutput;
      if (typeof updated === "string") {
        // The last opener, anchored at the end: an earlier one is command output.
        const text = String(output.output);
        const at = text.lastIndexOf(SHELL_METADATA_OPEN);
        const notice = at >= 0 && text.endsWith(SHELL_METADATA_CLOSE) ? text.slice(at) : "";
        output.output = updated + notice;
      }
    } catch (err) {
      // Fail open: the call already ran; failing it would discard its result,
      // and the full output stays reachable unsummarised.
      console.error(`spindrift-guards: ${SUMMARY_HOOK} failed, output left unchanged: ${err.message}`);
    }
  },
});
