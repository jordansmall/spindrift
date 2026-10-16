// opencode plugin (issue #5346). The claude PreToolUse hooks never run under
// opencode, so its bash tool would otherwise keep the Box's credentials and
// accept backgrounded work. This runs the very same baked hooks on each bash
// call, so the guard logic, its messages, and the credential list
// (credential-vars) stay single-sourced rather than re-implemented here.
// Plain ESM with node: builtins only: opencode's background `npm install` into
// this directory fails offline, so nothing here may import a package.
import { execFileSync } from "node:child_process";

// Baked for every Driver by lib/image.nix's homeLayoutScript.
const HOOKS_DIR = "/home/agent/.claude/hooks";

// Order matters: each hook sees the args the earlier ones rewrote, so the tee
// must stay last to wrap the scrubbed command. `rewrites`: silence from the
// scrub or tee on a non-empty command is a failure, not "allow".
const HOOKS = [
  { script: "reject-background-bash.sh", rewrites: false },
  { script: "env-credential-scrub.sh", rewrites: true },
  { script: "bash-output-tee.sh", rewrites: true },
];
const SUMMARY_HOOK = "bash-output-summary.sh";
// opencode appends a block opened by this and closed by SHELL_METADATA_CLOSE to
// a result it cut short (timeout, user abort); the summary replaces the whole
// output, so the notice is carried over.
const SHELL_METADATA_OPEN = "\n\n<shell_metadata>\n";
const SHELL_METADATA_CLOSE = "</shell_metadata>";

function runHook(script, args, onFailure = "") {
  const payload = JSON.stringify({ tool_name: "Bash", tool_input: args });
  let out;
  let parsed;
  try {
    out = execFileSync(`${HOOKS_DIR}/${script}`, [], {
      input: payload,
      encoding: "utf8",
      timeout: 10_000,
    });
    parsed = out.trim() === "" ? {} : JSON.parse(out);
  } catch (err) {
    throw new Error(`Bash hook ${script} failed: ${err.message}${onFailure}`);
  }
  return parsed.hookSpecificOutput ?? {};
}

export const SpindriftGuards = async () => ({
  "tool.execute.before": async (input, output) => {
    if (input.tool !== "bash") return;
    for (const { script, rewrites } of HOOKS) {
      // Fail closed: a hook that cannot run or answer must not wave the call
      // through with its credentials intact or without the log the tee keeps.
      const decision = runHook(script, output.args, ", refusing the call");
      if (decision.permissionDecision === "deny") {
        throw new Error(decision.permissionDecisionReason ?? `Bash guard ${script} denied the call`);
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
      const updated = runHook(SUMMARY_HOOK, input.args).updatedToolOutput;
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
