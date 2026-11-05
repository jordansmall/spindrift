// Behaviour test for the pi hook bridge's identical-tool-call loop breaker
// (issue #6384). BRIDGE is the bridge to load, its HOOKS_DIR staged with stubs.
// Most cases use an unmapped tool, which the bridge counts but never guards.
import assert from "node:assert/strict";

const { BRIDGE } = process.env;

async function freshHandler() {
  // A distinct query string gives each handler its own module instance.
  const mod = await import(`${BRIDGE}?${Math.random()}`);
  const handlers = {};
  mod.default({ on: (evt, fn) => (handlers[evt] = fn) });
  return handlers.tool_call;
}

const stderr = [];
const exits = [];
console.error = (line) => stderr.push(line);
// The real process.exit never returns; the stub throws so the handler stops too.
const EXITED = new Error("process.exit");
process.exit = (code) => {
  exits.push(code);
  throw EXITED;
};

const call = (handler, toolName, input) => handler({ toolName, input });

// Each blocked or shutdown call writes exactly one stderr line; allowed ones none.
async function allowed(handler, toolName, input) {
  stderr.length = 0;
  assert.equal(await call(handler, toolName, input), undefined, `expected allow for ${toolName}`);
  assert.deepEqual(stderr, []);
}
async function blocked(handler, toolName, input, count) {
  stderr.length = 0;
  const res = await call(handler, toolName, input);
  assert.equal(res?.block, true, `expected block of call ${count}`);
  assert.match(res.reason, /already made this exact call/);
  assert.match(res.reason, /different action/);
  assert.equal(stderr.length, 1);
  assert.match(stderr[0], /^spindrift-pi-hooks: /);
  assert.ok(stderr[0].includes(toolName), stderr[0]);
  assert.ok(stderr[0].includes(`${count}`), stderr[0]);
  assert.match(stderr[0], /block/);
}

let handler = await freshHandler();
await allowed(handler, "ls", { path: "/x" });
await allowed(handler, "ls", { path: "/x" });
await blocked(handler, "ls", { path: "/x" }, 3);
await blocked(handler, "ls", { path: "/x" }, 4);
assert.deepEqual(exits, []);

// The 5th identical call is the 3rd blocked in a row: shut the process down.
stderr.length = 0;
await assert.rejects(call(handler, "ls", { path: "/x" }), EXITED);
assert.equal(exits.length, 1);
assert.ok(exits[0] !== 0, "shutdown must exit nonzero");
assert.equal(stderr.length, 1);
assert.match(stderr[0], /^spindrift-pi-hooks: /);
assert.ok(stderr[0].includes("ls") && stderr[0].includes("5"), stderr[0]);
assert.match(stderr[0], /shutting down/);

// Different arguments, then a different tool, each reset the count.
exits.length = 0;
handler = await freshHandler();
await allowed(handler, "ls", { path: "/x" });
await allowed(handler, "ls", { path: "/x" });
await allowed(handler, "ls", { path: "/y" });
await allowed(handler, "ls", { path: "/y" });
await allowed(handler, "find", { path: "/y" });
await allowed(handler, "ls", { path: "/y" });
await allowed(handler, "ls", { path: "/y" });
await blocked(handler, "ls", { path: "/y" }, 3);
// A different call after blocks restarts the streak.
await allowed(handler, "ls", { path: "/z" });
await allowed(handler, "ls", { path: "/z" });
await blocked(handler, "ls", { path: "/z" }, 3);
assert.deepEqual(exits, []);

// The staged guards rewrite each bash call to a unique command; pi hands every
// call a fresh input, so the repeat must still be caught.
handler = await freshHandler();
await allowed(handler, "bash", { command: "find /work" });
await allowed(handler, "bash", { command: "find /work" });
await blocked(handler, "bash", { command: "find /work" }, 3);
assert.deepEqual(exits, []);

console.log("pi-hook-bridge-loop: ok");
