// Behaviour test for agent/opencode-guards.js (issue #5346), run by
// nix/checks/opencode-guards.nix under opencode's own runtime. Env in:
// PLUGIN (wired to the staged hooks), PLUGIN_MISSING, PLUGIN_GARBAGE and
// PLUGIN_SILENT (wired to a missing dir, to hooks that print non-JSON, and to
// hooks that exit 0 printing nothing), HOOKS_DIR.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { pathToFileURL } from "node:url";

async function load(path) {
  const mod = await import(pathToFileURL(path).href);
  const fns = Object.values(mod);
  assert.equal(fns.length, 1, "plugin must export exactly one function");
  assert.equal(typeof fns[0], "function");
  return (await fns[0]({}, {}))["tool.execute.before"];
}

const run = (hook, tool, args) => {
  const output = { args };
  return hook({ tool, sessionID: "s", callID: "c" }, output).then(() => output.args);
};

const hookDeny = (script, command) =>
  JSON.parse(
    execFileSync(`${process.env.HOOKS_DIR}/${script}`, [], {
      input: JSON.stringify({ tool_name: "Bash", tool_input: { command } }),
      encoding: "utf8",
    }),
  ).hookSpecificOutput.permissionDecisionReason;

// Every case runs even when an earlier one fails, so one red run reports all
// the breakage.
const cases = [];
const test = (name, fn) => cases.push([name, fn]);

const hook = await load(process.env.PLUGIN);

test("scrub rewrite drops credentials", async () => {
  const secrets = {
    OPENCODE_AUTH_CONTENT: "sekret-opencode",
    ANTHROPIC_API_KEY: "sekret-anthropic",
    CLAUDE_CODE_OAUTH_TOKEN: "sekret-oauth",
  };
  const args = { command: "env", timeout: 5, description: "dump env" };
  const rewritten = await run(hook, "bash", args);
  assert.equal(rewritten, args, "args must be mutated in place");
  assert.notEqual(rewritten.command, "env");
  assert.equal(rewritten.timeout, 5);
  const printed = execFileSync("bash", ["-c", rewritten.command], {
    encoding: "utf8",
    env: { ...process.env, ...secrets },
  });
  assert.ok(printed.includes("PATH="), "rewritten command must still run env");
  for (const [name, value] of Object.entries(secrets)) {
    assert.ok(!printed.includes(name), `${name} leaked`);
    assert.ok(!printed.includes(value), `${name} value leaked`);
  }
});

test("environ reads are refused with the scrub hook's message", async () => {
  const environCmd = "cat /proc/1/environ";
  await assert.rejects(run(hook, "bash", { command: environCmd }), {
    message: hookDeny("env-credential-scrub.sh", environCmd),
  });
  assert.match(hookDeny("env-credential-scrub.sh", environCmd), /environ/);
});

test("backgrounding is refused with the reject hook's message", async () => {
  const bgCmd = "sleep 1 &";
  await assert.rejects(run(hook, "bash", { command: bgCmd }), {
    message: hookDeny("reject-background-bash.sh", bgCmd),
  });
  assert.match(hookDeny("reject-background-bash.sh", bgCmd), /^Bash commands that self-background/);
});

test("other tools are untouched", async () => {
  assert.deepEqual(await run(hook, "read", { filePath: "/etc/hostname" }), { filePath: "/etc/hostname" });
});

// A silent reject hook is "no opinion", but a silent scrub hook would leave
// credentials intact, so only the latter must fail closed.
for (const [label, env] of [
  ["missing", "PLUGIN_MISSING"],
  ["garbage", "PLUGIN_GARBAGE"],
  ["silent", "PLUGIN_SILENT"],
]) {
  test(`${label} hooks fail closed`, async () => {
    const bad = await load(process.env[env]);
    await assert.rejects(run(bad, "bash", { command: "true" }), /refusing the call/);
    await run(bad, "read", { filePath: "x" });
  });
}

let failed = 0;
for (const [name, fn] of cases) {
  try {
    await fn();
    console.log(`ok ${name}`);
  } catch (err) {
    failed++;
    console.log(`FAIL ${name}: ${err.message}`);
  }
}
if (failed > 0) process.exit(1);
