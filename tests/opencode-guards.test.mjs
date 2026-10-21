// Behaviour test for agent/opencode-guards.js (issue #5346), run by
// nix/checks/opencode-guards.nix under opencode's own runtime. Env in:
// PLUGIN (wired to the staged hooks), PLUGIN_MISSING, PLUGIN_GARBAGE and
// PLUGIN_SILENT (wired to a missing dir, to hooks that print non-JSON, and to
// hooks that exit 0 printing nothing), PLUGIN_SILENT_TEE (the real hooks,
// except a tee that exits 0 printing nothing), CREDENTIAL_PATHS (the JSON of
// lib/credential-paths.nix), PLUGIN_UNMAPPED (a hook row naming an unmapped
// tool), HOOKS_DIR, BASH_TIMEOUT_CAP_ENV (the opencode registry's
// bashTimeoutEnv name, which the timeout-cap cases set). The summary hook reads
// BASH_OUTPUT_SUMMARY_TAIL_BYTES from this env; the test takes the bound from
// there, never from a constant of its own.
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { chmodSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
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

// The cap is read from the env on each call, so a case sets it for its own run.
const withCap = async (value, fn) => {
  const key = process.env.BASH_TIMEOUT_CAP_ENV;
  assert.ok(key, "BASH_TIMEOUT_CAP_ENV must name the cap var");
  const saved = process.env[key];
  if (value === undefined) delete process.env[key];
  else process.env[key] = value;
  try {
    return await fn();
  } finally {
    if (saved === undefined) delete process.env[key];
    else process.env[key] = saved;
  }
};

test("a bash timeout above the configured cap is clamped to it", async () => {
  const args = await withCap("1000", () => run(plugin.before, "bash", { command: "true", timeout: 600000 }));
  assert.equal(args.timeout, 1000);
});

test("a bash timeout at or below the cap is kept", async () => {
  for (const timeout of [1000, 400]) {
    const args = await withCap("1000", () => run(plugin.before, "bash", { command: "true", timeout }));
    assert.equal(args.timeout, timeout);
  }
});

test("an unset bash timeout stays unset under a cap", async () => {
  const args = await withCap("1000", () => run(plugin.before, "bash", { command: "true" }));
  assert.ok(!("timeout" in args));
});

test("no cap applies when the knob is unset or not a positive integer", async () => {
  for (const cap of [undefined, "", "abc", "0", "-5", "1.5", "10s"]) {
    const args = await withCap(cap, () => run(plugin.before, "bash", { command: "true", timeout: 600000 }));
    assert.equal(args.timeout, 600000, `cap ${JSON.stringify(cap)} must not clamp`);
  }
});

test("other tools are untouched", async () => {
  assert.deepEqual(await run(plugin.before, "glob", { pattern: ".env" }), { pattern: ".env" });
});

const credentialDeny = (tool, input) =>
  JSON.parse(
    execFileSync(`${process.env.HOOKS_DIR}/credential-deny.sh`, [], {
      input: JSON.stringify({ tool_name: tool, tool_input: input }),
      encoding: "utf8",
    }),
  ).hookSpecificOutput.permissionDecisionReason;

// Refused by the plugin's throw, which runs before opencode's permission check,
// so a Target-repo opencode.json allow rule has nothing to widen.
test("a plain cat of a credential file is refused with the hook's message", async () => {
  const command = "cat .env";
  const reason = credentialDeny("Bash", { command });
  assert.match(reason, /credential files/);
  await assert.rejects(run(plugin.before, "bash", { command }), { message: reason });
});

for (const command of [
  "cat .env*",
  "{ cat; } < .env",
  "x=$(<.env)",
  "while read l; do echo $l; done < .env",
]) {
  test(`bash \`${command}\` is refused`, async () => {
    await assert.rejects(run(plugin.before, "bash", { command }), {
      message: credentialDeny("Bash", { command }),
    });
  });
}

const credentialPaths = JSON.parse(process.env.CREDENTIAL_PATHS);
assert.ok(credentialPaths.length > 0, "CREDENTIAL_PATHS must be set");
for (const entry of credentialPaths) {
  // A glob entry stands for any concrete name it matches.
  const filePath = `/work/x/${entry.replaceAll("*", "local")}`;
  test(`read of ${filePath} is refused`, async () => {
    await assert.rejects(run(plugin.before, "read", { filePath }), {
      message: credentialDeny("Read", { file_path: filePath }),
    });
  });
}

test("read of a non-credential file is untouched", async () => {
  const args = { filePath: "/etc/hostname", offset: 1 };
  assert.deepEqual(await run(plugin.before, "read", args), { filePath: "/etc/hostname", offset: 1 });
});

// A guard without jq exits 0 and allows, so the plugin must probe jq itself.
// Bun resolves a child's executable from the PATH it started with, so each case
// is a fresh runtime whose whole PATH is the jq-less (or jq-broken) directory.
const jqDir = (script) => {
  const dir = mkdtempSync(join(tmpdir(), "jq-"));
  if (script !== undefined) {
    writeFileSync(join(dir, "jq"), script);
    chmodSync(join(dir, "jq"), 0o755);
  }
  return dir;
};
const probe = join(mkdtempSync(join(tmpdir(), "probe-")), "probe.mjs");
writeFileSync(
  probe,
  `const mod = await import(process.argv[2]);
const hooks = await Object.values(mod)[0]({}, {});
try {
  await hooks["tool.execute.before"]({ tool: process.argv[3] }, { args: { command: "true", filePath: "/etc/hostname" } });
  console.log("ALLOWED");
} catch (err) {
  console.log(err.message);
}
`,
);
const sh = execFileSync("bash", ["-c", "command -v bash"], { encoding: "utf8" }).trim();
for (const [label, dir] of [
  ["absent", jqDir()],
  ["broken", jqDir(`#!${sh}\nexit 1\n`)],
]) {
  for (const tool of ["bash", "read"]) {
    test(`${label} jq refuses a ${tool} call`, async () => {
      const ran = spawnSync(process.execPath, [probe, pathToFileURL(process.env.PLUGIN).href, tool], {
        encoding: "utf8",
        env: { ...process.env, BUN_BE_BUN: "1", PATH: dir },
      });
      assert.match(ran.stdout, /no working jq.*refusing the call/, ran.stderr);
    });
  }
}

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
    // Only the credential hook guards read, and silence from it is "allow".
    const readCall = run(bad.before, "read", { filePath: "x" });
    if (label === "missing" || label === "garbage") await assert.rejects(readCall, /refusing the call/);
    else await readCall;
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

// opencode skips a plugin whose load throws, leaving every guard off; a hook row
// naming an unmapped tool must still import and refuse every call, guarded or not.
test("an unmapped hook tool still loads and refuses every call", async () => {
  const bad = await load(process.env.PLUGIN_UNMAPPED);
  for (const tool of ["bash", "read", "glob"]) {
    await assert.rejects(run(bad.before, tool, { command: "true", filePath: "x" }), /nosuchtool.*no CLAUDE_TOOLS entry/);
  }
});

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
