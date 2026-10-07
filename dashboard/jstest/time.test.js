"use strict";

// Runs dashboard/web/static/time.js unmodified against a minimal fake DOM.
// Node built-ins only (ADR 0060): no npm, no jsdom.

// Pinned before any Date/Intl use so the expected zone is deterministic.
process.env.TZ = "America/New_York";

const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const SOURCE = fs.readFileSync(
  path.join(__dirname, "..", "web", "static", "time.js"),
  "utf8"
);

class FakeTime {
  constructor(datetime) {
    this.attrs = { datetime };
    this.textContent = datetime;
    this.title = "";
  }
  getAttribute(name) { return this.attrs[name]; }
}

class FakeRoot {
  constructor(times) { this.times = times; this.selectors = []; }
  querySelectorAll(sel) {
    this.selectors.push(sel);
    return this.times;
  }
}

function load(document) {
  const window = {};
  // time.js asks for the viewer's default locale; pin it so LANG cannot
  // change the zone name ("EST" vs "GMT-5") the assertions match.
  const PinnedIntl = {
    DateTimeFormat: function (locale, options) {
      return new Intl.DateTimeFormat(locale === undefined ? "en-US" : locale, options);
    },
  };
  const ctx = vm.createContext({ window, document, Date, Intl: PinnedIntl });
  vm.runInContext(SOURCE, ctx);
  return window;
}

const UTC = "2026-01-15T17:04:05Z";
const EXPECTED = new Intl.DateTimeFormat("en-US", {
  dateStyle: "medium",
  timeStyle: "long",
  timeZone: "America/New_York",
}).format(new Date(UTC));

test("localises to the viewer's zone, naming it, keeping UTC in the title", () => {
  const w = load(new FakeRoot([]));
  const el = new FakeTime(UTC);
  w.localizeTimes(new FakeRoot([el]));
  assert.notStrictEqual(el.textContent, UTC);
  assert.strictEqual(el.textContent, EXPECTED);
  assert.strictEqual(el.title, UTC);
  assert.strictEqual(el.getAttribute("datetime"), UTC);
});

test("an RFC3339 offset form converts to the same instant", () => {
  const w = load(new FakeRoot([]));
  const el = new FakeTime("2026-01-15T12:04:05.123-05:00");
  w.localizeTimes(new FakeRoot([el]));
  assert.strictEqual(el.textContent, EXPECTED);
});

test("a daylight-saving date names the daylight zone", () => {
  const w = load(new FakeRoot([]));
  const el = new FakeTime("2026-07-15T17:04:05Z");
  w.localizeTimes(new FakeRoot([el]));
  assert.match(el.textContent, /EDT/);
});

test("an unparseable value is left exactly as rendered", () => {
  const w = load(new FakeRoot([]));
  for (const bad of [
    "",
    "not a time",
    "2026-13-45T99:99:99Z",
    "1",
    "2026-10-07",
    "2026-10-07T09:00:00",
  ]) {
    const el = new FakeTime(bad);
    w.localizeTimes(new FakeRoot([el]));
    assert.strictEqual(el.textContent, bad);
    assert.strictEqual(el.title, "");
  }
});

test("a repeat call is idempotent", () => {
  const w = load(new FakeRoot([]));
  const el = new FakeTime(UTC);
  const root = new FakeRoot([el]);
  w.localizeTimes(root);
  const once = el.textContent;
  w.localizeTimes(root);
  assert.strictEqual(el.textContent, once);
  assert.strictEqual(el.title, UTC);
});

test("only time[datetime] elements are selected", () => {
  const w = load(new FakeRoot([]));
  const root = new FakeRoot([]);
  w.localizeTimes(root);
  assert.deepStrictEqual(root.selectors, ["time[datetime]"]);
});

test("runs over the document on load", () => {
  const el = new FakeTime(UTC);
  load(new FakeRoot([el]));
  assert.strictEqual(el.title, UTC);
  assert.match(el.textContent, /EST/);
});
