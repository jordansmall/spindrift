// Behaviour test for the pi hook bridge's subagent spawns gate (issue #5809).
// BRIDGE is the bridge staged with SPAWNS_FILE pointed at the baked policy;
// BRIDGE_MISSING is staged with it pointed at a file that does not exist, and
// BRIDGE_BAD at a policy whose value for the reviewer is a string, not a list.
import assert from "node:assert/strict";

const { BRIDGE, BRIDGE_MISSING, BRIDGE_BAD } = process.env;
const baseArgv = process.argv.slice();

async function handlerFor(path) {
  // A distinct query string gives each staged copy its own module instance.
  const mod = await import(`${path}?${Math.random()}`);
  const handlers = {};
  mod.default({ on: (evt, fn) => (handlers[evt] = fn) });
  return handlers.tool_call;
}

// A subagent child is told who it is only by the prompt file the extension
// writes for it, passed to its own pi process.
function asChild(name) {
  process.argv = [...baseArgv, "--append-system-prompt", `/tmp/pi-subagent-x/prompt-${name}.md`, "Task: go"];
}
function asTopLevel() {
  process.argv = baseArgv.slice();
}

const call = (handler, input) => handler({ toolName: "subagent", input });
const blocked = async (handler, input, ...needles) => {
  const res = await call(handler, input);
  assert.equal(res?.block, true, `expected block for ${JSON.stringify(input)}`);
  for (const n of needles) assert.ok(res.reason.includes(n), `reason ${JSON.stringify(res.reason)} lacks ${n}`);
};
const allowed = async (handler, input) => {
  assert.equal(await call(handler, input), undefined, `expected allow for ${JSON.stringify(input)}`);
};

const handler = await handlerFor(BRIDGE);

asChild("reviewer");
await blocked(handler, { agent: "worker", task: "x" }, "reviewer", "worker", "review-axis");
await blocked(handler, { tasks: [{ agent: "review-axis", task: "x" }, { agent: "worker", task: "x" }] }, "worker");
await blocked(handler, { chain: [{ agent: "worker", task: "x" }] }, "worker");
await blocked(handler, { agent: "review-axis", agentScope: "project", task: "x" }, "agentScope");
await allowed(handler, { agent: "review-axis", task: "x" });
await allowed(handler, { tasks: [{ agent: "review-axis", task: "x" }], chain: [{ agent: "review-axis", task: "x" }] });

asChild("scout");
await blocked(handler, { agent: "worker", task: "x" }, "scout", "none");

asChild("worker");
await allowed(handler, { agent: "worker", task: "x" });

asTopLevel();
await allowed(handler, { agent: "worker", task: "x" });
await blocked(handler, { agent: "worker", agentScope: "both", task: "x" }, "agentScope");

const missing = await handlerFor(BRIDGE_MISSING);
asTopLevel();
await allowed(missing, { agent: "worker", task: "x" });
asChild("reviewer");
await blocked(missing, { agent: "review-axis", task: "x" }, "spawns policy");

const bad = await handlerFor(BRIDGE_BAD);
asChild("reviewer");
await blocked(bad, { agent: "review-axis", task: "x" }, "reviewer", "not a list");
asChild("scout");
await allowed(bad, { agent: "worker", task: "x" });

console.log("pi-hook-bridge-spawns: ok");
