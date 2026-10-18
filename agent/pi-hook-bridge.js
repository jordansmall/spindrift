// pi extension (issue #5482). The claude hooks never run under pi, so its
// bash and read tools would otherwise keep the Box's credentials, accept
// backgrounded work and reach credential files. This runs the very same baked
// hooks around each call, so the guard logic, its messages, and the credential
// list (credential-vars) stay single-sourced rather than re-implemented here.
// It also blocks a `subagent` call whose agentScope is not "user", keeping the
// roster to the baked agents.
// Plain ESM with node: builtins only, because the Box is offline and pi loads
// the file as is.
import { execFileSync } from "node:child_process";

// Baked for every Driver by lib/image.nix's homeLayoutScript.
const HOOKS_DIR = "/home/agent/.claude/hooks";
// Bounds a hung guard like lib/image.nix's guardBound and opencode-guards.js:
// Claude's own hook timeout is non-blocking, so unbounded it would let the call run.
const GUARD_TIMEOUT_MS = 10_000;
// The summary hook is a PostToolUse hook, which lib/image.nix leaves at
// Claude's default timeout.
const SUMMARY_TIMEOUT_MS = 60_000;

// Generated from lib/bash-hooks.nix, which documents the run order, `tools` and
// `rewrites`.
// BEGIN GENERATED PI HOOKS -- nix run .#regen -- DO NOT EDIT
const HOOKS = [
  { script: "reject-background-bash.sh", tools: ["bash"], rewrites: false },
  { script: "credential-deny.sh", tools: ["read", "bash"], rewrites: false },
  { script: "env-credential-scrub.sh", tools: ["bash"], rewrites: true },
  { script: "bash-output-tee.sh", tools: ["bash"], rewrites: true },
];
const SUMMARY_HOOK = "bash-output-summary.sh";
// END GENERATED PI HOOKS

// The hooks read claude's tool names and argument names; pi's read tool calls
// its path `path`, where claude's calls it `file_path`.
const CLAUDE_TOOLS = {
  bash: { name: "Bash", input: (args) => args },
  read: { name: "Read", input: (args) => ({ file_path: args.path }) },
};

// Every tool a generated hook names must be mapped: the handler skips unmapped
// tools, which would leave one silently unguarded.
for (const { script, tools } of HOOKS) {
  for (const tool of tools) {
    if (!Object.hasOwn(CLAUDE_TOOLS, tool)) {
      throw new Error(`spindrift-pi-hooks: ${script} names tool ${tool} with no CLAUDE_TOOLS entry`);
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

function runHook(script, toolName, args, timeout, onFailure = "") {
  const tool = CLAUDE_TOOLS[toolName];
  const payload = JSON.stringify({ tool_name: tool.name, tool_input: tool.input(args) });
  let out;
  let parsed;
  try {
    out = execFileSync(`${HOOKS_DIR}/${script}`, [], {
      input: payload,
      encoding: "utf8",
      timeout,
    });
    parsed = out.trim() === "" ? {} : JSON.parse(out);
  } catch (err) {
    throw new Error(`${tool.name} hook ${script} failed: ${err.message}${onFailure}`);
  }
  return parsed.hookSpecificOutput ?? {};
}

export default function (pi) {
  pi.on("tool_call", async (event) => {
    // agentScope is model-supplied and the extension confirms project agents
    // only when a UI exists, which a Box lacks: "project" or "both" would let
    // the Target repo's .pi/agents override the baked roster.
    if (event.toolName === "subagent" && (event.input?.agentScope ?? "user") !== "user") {
      return { block: true, reason: "Refusing agentScope other than user: project-local agents are untrusted, since the Target repo is untrusted" };
    }
    if (!Object.hasOwn(CLAUDE_TOOLS, event.toolName)) return;
    requireJq();
    for (const { script, tools, rewrites } of HOOKS) {
      if (!tools.includes(event.toolName)) continue;
      // Fail closed: pi blocks a call whose handler throws, and a hook that
      // cannot run or answer must not wave the call through with its
      // credentials intact or without the log the tee keeps.
      const decision = runHook(script, event.toolName, event.input, GUARD_TIMEOUT_MS, ", refusing the call");
      if (decision.permissionDecision === "deny") {
        return { block: true, reason: decision.permissionDecisionReason ?? `Guard ${script} denied the call` };
      }
      // Mutate in place: pi executes the tool with this same input object.
      if (typeof decision.updatedInput?.command === "string") {
        Object.assign(event.input, decision.updatedInput);
      } else if (rewrites && typeof event.input.command === "string" && event.input.command !== "") {
        // The rewriting hooks exit silently for an empty command only.
        throw new Error(`Bash guard ${script} returned no rewrite, refusing the call`);
      }
    }
  });
  // The summary hook owns the output bound (BASH_OUTPUT_SUMMARY_TAIL_BYTES); a
  // short result passes through untouched. event.input is the object the
  // tool_call handler rewrote, so it still carries the tee wrapper the summary
  // parses.
  pi.on("tool_result", async (event) => {
    if (event.toolName !== "bash") return;
    try {
      const updated = runHook(SUMMARY_HOOK, event.toolName, event.input, SUMMARY_TIMEOUT_MS).updatedToolOutput;
      if (typeof updated === "string") {
        return { content: [{ type: "text", text: updated }] };
      }
    } catch (err) {
      // Fail open: the call already ran, and pi would only log a throw here and
      // keep the unsummarised result; the full output stays reachable.
      console.error(`spindrift-pi-hooks: ${SUMMARY_HOOK} failed, output left unchanged: ${err.message}`);
    }
  });
}
