"use strict";

// Runs dashboard/web/static/live.js unmodified against a minimal fake DOM.
// Node built-ins only (ADR 0060): no npm, no jsdom.
//
// Every pane swap, insert and localizeTimes call lands in one ordered log,
// so a test can pin that new markup is localised before the viewer sees it.

const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const SOURCE = fs.readFileSync(
  path.join(__dirname, "..", "web", "static", "live.js"),
  "utf8"
);

class FakeEventSource {
  constructor() { this.handlers = {}; }
  addEventListener(type, fn) { this.handlers[type] = fn; }
  emit(type, data) { this.handlers[type]({ type, data }); }
}

class FakeRow {
  constructor() {
    this.classes = [];
    this.classList = { add: (c) => this.classes.push(c) };
  }
  remove() {}
}

// A pane whose innerHTML setter logs instead of parsing markup.
class FakePane {
  constructor(log, dataset) {
    this.log = log;
    this.dataset = dataset || {};
    this.empty = null;
    this.entries = [];
  }
  set innerHTML(html) { this.log.push(["innerHTML", this, html]); }
  querySelectorAll(sel) { return sel === ".entry" ? this.entries.slice() : []; }
  querySelector(sel) {
    if (sel === ".empty") return this.empty;
    if (sel === ".entry") return this.entries[0] || null;
    return null;
  }
  insertBefore(row, ref) {
    this.log.push(["insertBefore", row, ref]);
    const at = ref ? this.entries.indexOf(ref) : this.entries.length;
    this.entries.splice(at, 0, row);
  }
}

function load({ localize = true } = {}) {
  const log = [];
  const status = new FakePane(log);
  const history = new FakePane(log, { limit: "50" });
  const body = { classList: { add() {}, remove() {} } };
  const row = new FakeRow();
  const sources = [];
  function FakeES() {
    const es = new FakeEventSource();
    sources.push(es);
    return es;
  }
  const document = {
    body,
    getElementById: (id) => ({ status, history })[id] || null,
    createElement(tag) {
      assert.strictEqual(tag, "template");
      const t = { content: { firstElementChild: null } };
      // Like a real <template>, whitespace alone parses to no element.
      Object.defineProperty(t, "innerHTML", {
        set(html) { t.content.firstElementChild = html.trim() === "" ? null : row; },
      });
      return t;
    },
  };
  const window = { EventSource: FakeES };
  if (localize) window.localizeTimes = (el) => log.push(["localize", el]);
  const ctx = vm.createContext({
    window,
    document,
    EventSource: FakeES,
    Date,
    parseInt,
  });
  vm.runInContext(SOURCE, ctx);
  assert.strictEqual(sources.length, 1);
  return { es: sources[0], log, status, history, row };
}

// strictEqual per field, so a logged fake must be the very element
// expected, not merely one with the same fields.
function assertLog(log, want) {
  assert.strictEqual(log.length, want.length, "log length");
  want.forEach((w, i) => {
    assert.strictEqual(log[i].length, w.length, `log[${i}] length`);
    w.forEach((v, j) => assert.strictEqual(log[i][j], v, `log[${i}][${j}]`));
  });
}

test("a status event localises the status pane after swapping it in", () => {
  const { es, log, status } = load();
  es.emit("status", "<p>x</p>");
  assertLog(log, [["innerHTML", status, "<p>x</p>"], ["localize", status]]);
});

test("a history event localises the history pane after swapping it in", () => {
  const { es, log, history } = load();
  es.emit("history", "<li class=entry>x</li>");
  assertLog(log, [
    ["innerHTML", history, "<li class=entry>x</li>"],
    ["localize", history],
  ]);
});

test("an entry into an empty history drops the placeholder, then localises the row before inserting it", () => {
  const { es, log, history, row } = load();
  history.empty = { remove: () => log.push(["empty.remove"]) };
  es.emit("entry", "<li class=entry>x</li>");
  assertLog(log, [
    ["empty.remove"],
    ["localize", row],
    ["insertBefore", row, null],
  ]);
  assert.deepStrictEqual(row.classes, ["fresh"]);
});

test("an entry above existing rows is localised before it is inserted ahead of the first", () => {
  const { es, log, history, row } = load();
  const first = new FakeRow();
  history.entries = [first, new FakeRow()];
  es.emit("entry", "<li class=entry>x</li>");
  assertLog(log, [
    ["localize", row],
    ["insertBefore", row, first],
  ]);
  assert.strictEqual(history.entries[0], row);
  assert.deepStrictEqual(row.classes, ["fresh"]);
});

test("an entry with blank or whitespace-only data localises and inserts nothing", () => {
  for (const data of ["", "   ", "\n\t "]) {
    const { es, log } = load();
    es.emit("entry", data);
    assertLog(log, []);
  }
});

test("without window.localizeTimes every event still applies", () => {
  const { es, log, status, history, row } = load({ localize: false });
  es.emit("status", "<p>x</p>");
  es.emit("history", "<li class=entry>x</li>");
  es.emit("entry", "<li class=entry>y</li>");
  assertLog(log, [
    ["innerHTML", status, "<p>x</p>"],
    ["innerHTML", history, "<li class=entry>x</li>"],
    ["insertBefore", row, null],
  ]);
  assert.deepStrictEqual(row.classes, ["fresh"]);
});
