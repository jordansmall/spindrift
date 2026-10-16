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

// Order matters only for which denial wins; neither hook reads the other's
// rewrite. `rewrites`: the hook must answer a non-empty command with a rewrite
// (silence would let the call run with credentials intact), unlike the reject
// hook, where silence means allow.
const HOOKS = [
  { script: "reject-background-bash.sh", rewrites: false },
  { script: "env-credential-scrub.sh", rewrites: true },
];

function runHook(script, args) {
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
    // Fail closed: a hook that cannot run or answer must not wave the call
    // through with its credentials intact.
    throw new Error(`Bash guard ${script} failed, refusing the call: ${err.message}`);
  }
  return parsed.hookSpecificOutput ?? {};
}

export const SpindriftGuards = async () => ({
  "tool.execute.before": async (input, output) => {
    if (input.tool !== "bash") return;
    for (const { script, rewrites } of HOOKS) {
      const decision = runHook(script, output.args);
      if (decision.permissionDecision === "deny") {
        throw new Error(decision.permissionDecisionReason ?? `Bash guard ${script} denied the call`);
      }
      // Mutate in place: opencode executes the tool with this same args object.
      if (typeof decision.updatedInput?.command === "string") {
        output.args.command = decision.updatedInput.command;
      } else if (rewrites && typeof output.args.command === "string" && output.args.command !== "") {
        // The scrub hook exits silently for an empty command only.
        throw new Error(`Bash guard ${script} returned no rewrite, refusing the call`);
      }
    }
  },
});
