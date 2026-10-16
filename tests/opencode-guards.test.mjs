// Behaviour test for agent/opencode-guards.js (issue #5346), run by
// nix/checks/opencode-guards.nix under opencode's own runtime. Env in:
// PLUGIN (wired to the staged hooks), PLUGIN_MISSING, PLUGIN_GARBAGE and
// PLUGIN_SILENT (wired to a missing dir, to hooks that print non-JSON, and to
// hooks that exit 0 printing nothing), PLUGIN_SILENT_TEE (the real hooks, except a
// tee that exits 0 printing nothing), HOOKS_DIR. The summary hook reads
// BASH_OUTPUT_SUMMARY_TAIL_BYTES from this env; the test takes the bound from
// there, never from a constant of its own.
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

async function load(path) {
  const mod = await import(pathToFileURL(path).href);
  const fns = Object.values(mod);
  assert.equal(fns.length, 1, "plugin must export exactly one function");
  assert.equal(typeof fns[0], "function");
  const hooks = await fns[0]({}, {});
  return { before: hooks["tool.execute.before"], after: hooks["tool.execute.after"] };
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

const plugin = await load(process.env.PLUGIN);
const tailBytes = Number(process.env.BASH_OUTPUT_SUMMARY_TAIL_BYTES);
assert.ok(tailBytes > 0, "BASH_OUTPUT_SUMMARY_TAIL_BYTES must be set");

// Runs a bash call the way opencode does: the before hook rewrites args, the
// rewritten command runs under bash, then the after hook sees the result plus
// whatever `suffix` opencode appends (its <shell_metadata> trailer).
const bashCall = async (command, suffix = "") => {
  const args = { command };
  await run(plugin.before, "bash", args);
  const ran = spawnSync("bash", ["-c", args.command], { encoding: "utf8" });
  const output = { title: command, output: ran.stdout + suffix, metadata: {} };
  await plugin.after({ tool: "bash", sessionID: "s", callID: "c", args }, output);
  return { output, raw: ran.stdout, status: ran.status };
};

test("scrub rewrite drops credentials", async () => {
  const secrets = {
    OPENCODE_AUTH_CONTENT: "sekret-opencode",
    ANTHROPIC_API_KEY: "sekret-anthropic",
    CLAUDE_CODE_OAUTH_TOKEN: "sekret-oauth",
  };
  const args = { command: "env", timeout: 5, description: "dump env" };
  const rewritten = await run(plugin.before, "bash", args);
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
  await assert.rejects(run(plugin.before, "bash", { command: environCmd }), {
    message: hookDeny("env-credential-scrub.sh", environCmd),
  });
  assert.match(hookDeny("env-credential-scrub.sh", environCmd), /environ/);
});

test("backgrounding is refused with the reject hook's message", async () => {
  const bgCmd = "sleep 1 &";
  await assert.rejects(run(plugin.before, "bash", { command: bgCmd }), {
    message: hookDeny("reject-background-bash.sh", bgCmd),
  });
  assert.match(hookDeny("reject-background-bash.sh", bgCmd), /^Bash commands that self-background/);
});

test("other tools are untouched", async () => {
  assert.deepEqual(await run(plugin.before, "read", { filePath: "/etc/hostname" }), { filePath: "/etc/hostname" });
});

test("long output is bounded to a tail with the full log on disk", async () => {
  const command = "seq 1 2000; exit 3";
  const full = execFileSync("seq", ["1", "2000"], { encoding: "utf8" });
  const { output, raw, status } = await bashCall(command);
  assert.equal(status, 3, "exit code must survive the tee wrapper");
  assert.equal(raw, full);
  assert.ok(raw.length > tailBytes, "fixture must exceed the bound");
  assert.match(output.output, /^exit code: 3$/m);
  const logFile = output.output.match(/^log file: (\S+)$/m)?.[1];
  assert.ok(logFile, "summary must name the log");
  assert.equal(readFileSync(logFile, "utf8"), full);
  // Everything after the marker is exactly the tail: bounded, no middle.
  const body = output.output.split(`--- last ${tailBytes} bytes of output ---\n`)[1];
  assert.equal(body, full.slice(-tailBytes).trimEnd());
});

test("opencode's shell_metadata trailer survives the summary", async () => {
  const trailer =
    "\n\n<shell_metadata>\nshell tool terminated command after exceeding timeout 1000 ms. " +
    "If this command is expected to take longer and is not waiting for interactive input, " +
    "retry with a larger timeout value in milliseconds.\n</shell_metadata>";
  const { output } = await bashCall("seq 1 2000", trailer);
  assert.match(output.output, /^log file: \S+$/m, "output must be summarised");
  assert.ok(output.output.endsWith(trailer), "trailer must be carried over");
  assert.equal(output.output.split("<shell_metadata>").length, 2, "trailer must appear once");
});

test("only the final shell_metadata block is carried over", async () => {
  const trailer = "\n\n<shell_metadata>\nreal notice\n</shell_metadata>";
  const earlier = "\n\n<shell_metadata>\n" + "y".repeat(tailBytes * 8) + "\n</shell_metadata>\nmore output";
  const { output } = await bashCall("seq 1 2000", earlier + trailer);
  assert.match(output.output, /^log file: \S+$/m, "output must be summarised");
  assert.ok(output.output.endsWith(trailer), "the final trailer must be carried over");
  assert.equal(output.output.split("<shell_metadata>").length, 2, "trailer must appear once");
  assert.ok(!output.output.includes("yyyy"), "earlier block must not be carried over");
});

test("short output is returned unchanged", async () => {
  const { output, raw } = await bashCall("echo hi");
  assert.equal(raw, "hi\n");
  assert.equal(output.output, "hi\n");
});

test("the after hook ignores other tools", async () => {
  const output = { title: "t", output: "x".repeat(tailBytes * 4), metadata: {} };
  const before = output.output;
  await plugin.after({ tool: "read", sessionID: "s", callID: "c", args: { filePath: "x" } }, output);
  assert.equal(output.output, before);
});

// A silent reject hook is "no opinion", but a silent scrub hook would leave
// credentials intact, so only the latter must fail closed. The silent-tee
// plugin isolates the tee: reject and scrub are real, so only its own check
// can refuse the call.
for (const [label, env] of [
  ["missing", "PLUGIN_MISSING"],
  ["garbage", "PLUGIN_GARBAGE"],
  ["silent", "PLUGIN_SILENT"],
  ["silent-tee", "PLUGIN_SILENT_TEE"],
]) {
  test(`${label} hooks fail closed`, async () => {
    const bad = await load(process.env[env]);
    await assert.rejects(run(bad.before, "bash", { command: "true" }), /refusing the call/);
    await run(bad.before, "read", { filePath: "x" });
  });

  // The call already ran, so a broken summary hook must not fail it. Skipped for
  // silent-tee: it carries the real summary hook, so this would pass only
  // because the empty log is under the bound.
  if (label !== "silent-tee") test(`${label} summary hook leaves the output unchanged`, async () => {
    const bad = await load(process.env[env]);
    // The good before hook only supplies a tee-wrapped command for the summary
    // hook to parse; that command never runs.
    const args = { command: "seq 1 2000" };
    await run(plugin.before, "bash", args);
    const raw = "x".repeat(tailBytes * 4);
    const output = { title: "t", output: raw, metadata: {} };
    await bad.after({ tool: "bash", sessionID: "s", callID: "c", args }, output);
    assert.equal(output.output, raw);
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
