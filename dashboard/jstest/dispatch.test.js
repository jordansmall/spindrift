"use strict";

// Runs dashboard/web/static/dispatch.js unmodified against a minimal fake DOM.
// Node built-ins only (ADR 0060): no npm, no jsdom.

const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "web", "static");
const SOURCE = fs.readFileSync(path.join(STATIC, "dispatch.js"), "utf8");
const STYLE = fs.readFileSync(path.join(STATIC, "style.css"), "utf8");

class Event {
  constructor(type, init) {
    this.type = type;
    this.data = init && init.data;
  }
}

class Text {
  constructor(data) {
    this.nodeType = 3;
    this.data = data;
    this.parentNode = null;
  }
  get textContent() { return this.data; }
}

// Shared by elements and fragments: child bookkeeping and event listeners.
class Container {
  constructor(nodeType) {
    this.nodeType = nodeType;
    this.parentNode = null;
    this.childNodes = [];
    this.listeners = {};
  }

  get children() { return this.childNodes.filter((n) => n.nodeType === 1); }

  get textContent() { return this.childNodes.map((n) => n.textContent).join(""); }
  set textContent(v) {
    this.childNodes.forEach((n) => { n.parentNode = null; });
    this.childNodes = [];
    if (v !== "") this.appendChild(new Text(String(v)));
  }

  // dispatch.js must build the DOM from createElement/createTextNode only
  // (see its untrusted-log header comment), so any innerHTML write is a test failure.
  set innerHTML(v) { throw new Error("innerHTML written: " + v); }

  insertBefore(node, ref) {
    if (node.nodeType === 11) {
      const moved = node.childNodes.slice();
      node.childNodes = [];
      moved.forEach((c) => { c.parentNode = null; this.insertBefore(c, ref); });
      return node;
    }
    if (node.parentNode) node.parentNode.removeChild(node);
    const at = ref ? this.childNodes.indexOf(ref) : this.childNodes.length;
    assert.ok(at >= 0, "insertBefore: ref is not a child");
    this.childNodes.splice(at, 0, node);
    node.parentNode = this;
    return node;
  }

  appendChild(node) { return this.insertBefore(node, null); }

  removeChild(node) {
    const at = this.childNodes.indexOf(node);
    assert.ok(at >= 0, "removeChild: not a child");
    this.childNodes.splice(at, 1);
    node.parentNode = null;
    return node;
  }

  addEventListener(type, fn, opts) {
    (this.listeners[type] = this.listeners[type] || []).push({
      fn,
      once: !!(opts && opts.once),
    });
  }

  dispatchEvent(ev) {
    ev.target = this;
    const list = (this.listeners[ev.type] || []).slice();
    for (const l of list) {
      if (l.once) {
        this.listeners[ev.type] = this.listeners[ev.type].filter((x) => x !== l);
      }
      l.fn.call(this, ev);
    }
    return true;
  }
}

class Element extends Container {
  constructor(tagName) {
    super(1);
    this.tagName = tagName.toUpperCase();
    this.className = "";
    this.style = {};
    this.dataset = {};
    this.attrs = {};
    this.hidden = false;
    this.checked = false;
    this.scrollTop = 0;
    this.scrollHeight = 0;
    this.clientHeight = 0;
    const self = this;
    this.classList = {
      contains(name) { return self.classes().includes(name); },
      toggle(name, force) {
        const names = self.classes();
        const want = force === undefined ? !names.includes(name) : !!force;
        const rest = names.filter((n) => n !== name);
        self.className = (want ? rest.concat(name) : rest).join(" ");
        return want;
      },
    };
  }

  classes() { return this.className.split(/\s+/).filter(Boolean); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  hasAttribute(k) { return k in this.attrs; }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }

  // Only the two selector shapes dispatch.js uses: ".cls" and "tag.cls".
  matches(sel) {
    const m = /^([a-z]*)\.([\w-]+)$/.exec(sel);
    assert.ok(m, "unsupported selector " + sel);
    return (m[1] === "" || this.tagName === m[1].toUpperCase()) &&
      this.classes().includes(m[2]);
  }

  querySelectorAll(sel) {
    const out = [];
    const walk = (node) => {
      node.children.forEach((c) => {
        if (c.matches(sel)) out.push(c);
        walk(c);
      });
    };
    walk(this);
    return out;
  }

  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
}

class FakeEventSource {
  constructor(url) {
    this.url = url;
    this.readyState = 1;
    this.listeners = {};
    this.onopen = null;
    this.onerror = null;
    FakeEventSource.instances.push(this);
  }
  addEventListener(type, fn) {
    (this.listeners[type] = this.listeners[type] || []).push(fn);
  }
  emit(type, data) {
    (this.listeners[type] || []).forEach((fn) => fn({ type, data }));
  }
  close() { this.readyState = FakeEventSource.CLOSED; }
}
FakeEventSource.CLOSED = 2;
FakeEventSource.instances = [];

// Builds one .tabbody pane, runs dispatch.js against it, and opens the stream.
function page(opts) {
  opts = opts || {};
  const body = new Element("body");
  const section = new Element("section");
  section.className = "tabbody";
  section.id = "tab-log";
  const log = new Element("pre");
  log.className = "log";
  log.dataset.src = "/log/stream";
  if (opts.filter) log.setAttribute("data-stream-filter", "");
  section.appendChild(log);
  let showAll = null;
  if (opts.showAll) {
    showAll = new Element("input");
    showAll.className = "show-all";
    section.appendChild(showAll);
  }
  const followState = new Element("span");
  followState.className = "follow-state";
  section.appendChild(followState);
  let tab = null;
  if (opts.tab) {
    tab = new Element("button");
    tab.className = "tab";
    tab.setAttribute("data-tab", "log");
    body.appendChild(tab);
  }
  body.appendChild(section);
  section.hidden = !!opts.hidden;

  const frames = [];
  let clock = 0;
  FakeEventSource.instances = [];
  const sandbox = {
    document: {
      createElement: (tag) => new Element(tag),
      createTextNode: (data) => new Text(data),
      createDocumentFragment: () => new Container(11),
      querySelectorAll: (sel) => body.querySelectorAll(sel),
    },
    window: { EventSource: FakeEventSource },
    EventSource: FakeEventSource,
    Event,
    // Each read advances the clock by opts.clockStep ms; the default 0 never
    // trips the 12ms per-frame budget, so one frame drains everything.
    performance: { now: () => { const t = clock; clock += opts.clockStep || 0; return t; } },
    requestAnimationFrame: (fn) => { frames.push(fn); },
  };
  vm.runInNewContext(SOURCE, sandbox);

  const es = FakeEventSource.instances[0];
  const drain = () => { while (frames.length) frames.shift()(); };
  if (es) es.onopen();
  return {
    section, log, showAll, followState, es, tab, frames, drain,
    feed(text) { es.emit("log", text); drain(); },
    lines() { return log.children; },
    jump() { return followState.children.find((c) => c.className === "jump"); },
    scrollEvent() { log.dispatchEvent(new Event("scroll")); },
    // Scrolls away from the end, which pauses following.
    pause() {
      log.scrollHeight = 1000;
      log.clientHeight = 100;
      log.scrollTop = 0;
      this.scrollEvent();
    },
  };
}

// One line's rendered pieces: bare text as a string, spans as {text, style}.
function pieces(line) {
  return line.childNodes.map((n) =>
    n.nodeType === 3 ? n.data : { text: n.textContent, style: Object.assign({}, n.style) });
}

function one(text) {
  const p = page();
  p.feed(text + "\n");
  assert.strictEqual(p.lines().length, 1);
  return pieces(p.lines()[0]);
}

function style(piece) {
  assert.strictEqual(typeof piece, "object", "expected a styled span");
  return piece.style;
}

test("plain text is a bare text node, not a span", () => {
  assert.deepStrictEqual(one("hello"), ["hello"]);
});

test("bold, dim, italic and underline become span styles", () => {
  assert.strictEqual(style(one("\x1b[1mx")[0]).fontWeight, "bold");
  assert.strictEqual(style(one("\x1b[2mx")[0]).opacity, "0.6");
  assert.strictEqual(style(one("\x1b[3mx")[0]).fontStyle, "italic");
  assert.strictEqual(style(one("\x1b[4mx")[0]).textDecoration, "underline");
  const both = style(one("\x1b[1;4mx")[0]);
  assert.strictEqual(both.fontWeight, "bold");
  assert.strictEqual(both.textDecoration, "underline");
});

test("16-colour foreground and background, normal and bright", () => {
  assert.strictEqual(style(one("\x1b[31mx")[0]).color, "#e06c75");
  assert.strictEqual(style(one("\x1b[91mx")[0]).color, "#ff8b94");
  assert.strictEqual(style(one("\x1b[30mx")[0]).color, "#3d4556");
  assert.strictEqual(style(one("\x1b[97mx")[0]).color, "#ffffff");
  assert.strictEqual(style(one("\x1b[42mx")[0]).backgroundColor, "#98c379");
  assert.strictEqual(style(one("\x1b[102mx")[0]).backgroundColor, "#b5e890");
});

test("256-colour: palette, cube, greyscale ramp, and clamp past 255", () => {
  assert.strictEqual(style(one("\x1b[38;5;4mx")[0]).color, "#61afef");
  assert.strictEqual(style(one("\x1b[38;5;196mx")[0]).color, "rgb(255,0,0)");
  assert.strictEqual(style(one("\x1b[38;5;232mx")[0]).color, "rgb(8,8,8)");
  assert.strictEqual(style(one("\x1b[38;5;300mx")[0]).color, "rgb(238,238,238)");
  assert.strictEqual(style(one("\x1b[48;5;196mx")[0]).backgroundColor, "rgb(255,0,0)");
});

test("truecolour sets fg and bg, clamping channels above 255", () => {
  assert.strictEqual(style(one("\x1b[38;2;10;20;30mx")[0]).color, "rgb(10,20,30)");
  assert.strictEqual(style(one("\x1b[48;2;1;2;3mx")[0]).backgroundColor, "rgb(1,2,3)");
  assert.strictEqual(style(one("\x1b[38;2;300;20;30mx")[0]).color, "rgb(255,20,30)");
});

test("0 and an empty parameter list both reset", () => {
  const a = one("\x1b[1;31mA\x1b[0mB");
  assert.strictEqual(a[0].text, "A");
  assert.strictEqual(a[1], "B");
  const b = one("\x1b[1;31mA\x1b[mB");
  assert.strictEqual(b[1], "B");
});

test("22, 23, 24, 39 and 49 switch their attribute off", () => {
  assert.strictEqual(one("\x1b[1;2m\x1b[22mx")[0], "x");
  assert.strictEqual(one("\x1b[3m\x1b[23mx")[0], "x");
  assert.strictEqual(one("\x1b[4m\x1b[24mx")[0], "x");
  assert.strictEqual(one("\x1b[31m\x1b[39mx")[0], "x");
  assert.strictEqual(one("\x1b[41m\x1b[49mx")[0], "x");
  // Switching one off leaves the others on.
  const s = style(one("\x1b[1;3m\x1b[23mx")[0]);
  assert.strictEqual(s.fontWeight, "bold");
  assert.strictEqual(s.fontStyle, undefined);
});

test("a malformed 38/48 ignores the remaining parameters", () => {
  assert.deepStrictEqual(one("\x1b[38;7;1mx"), ["x"]);
  assert.deepStrictEqual(one("\x1b[48;5mx"), ["x"]);
  assert.deepStrictEqual(one("\x1b[38;2;1;2mx"), ["x"]);
  // Parameters before the malformed one still apply.
  const s = style(one("\x1b[1;38;7;4mx")[0]);
  assert.strictEqual(s.fontWeight, "bold");
  assert.strictEqual(s.textDecoration, undefined);
});

test("SGR state carries across committed lines", () => {
  const p = page();
  p.feed("\x1b[31mone\ntwo\n");
  assert.strictEqual(style(pieces(p.lines()[0])[0]).color, "#e06c75");
  assert.strictEqual(style(pieces(p.lines()[1])[0]).color, "#e06c75");
  p.feed("\x1b[0mthree\n");
  assert.deepStrictEqual(pieces(p.lines()[2]), ["three"]);
});

test("escape sequences other than SGR never reach the text", () => {
  const text = (s) => one(s).map((x) => (typeof x === "string" ? x : x.text)).join("");
  assert.strictEqual(text("a\x1b[2Kb"), "ab");
  assert.strictEqual(text("a\x1b[1;5Hb"), "ab");
  assert.strictEqual(text("a\x1b]0;title\x07b"), "ab");
  assert.strictEqual(text("a\x1b]0;title\x1b\\b"), "ab");
  assert.strictEqual(text("a\x1b]0;unterminated and the rest"), "a");
  assert.strictEqual(text("a\x1b[3"), "a");
  assert.strictEqual(text("a\x1bb"), "ab");
});

test("hostile markup is rendered as text and innerHTML is never touched", () => {
  const p = page();
  p.feed("<img src=x onerror=alert(1)>\x1b[31m<b>x</b>\n");
  const line = p.lines()[0];
  assert.strictEqual(line.textContent, "<img src=x onerror=alert(1)><b>x</b>");
  assert.strictEqual(line.children.length, 1);
  assert.strictEqual(line.children[0].tagName, "SPAN");
});

test("lines split on newline, CR is stripped, the unfinished tail is held", () => {
  const p = page();
  p.feed("ab\r\ncd");
  assert.strictEqual(p.lines().length, 2);
  assert.strictEqual(p.lines()[0].textContent, "ab");
  assert.strictEqual(p.lines()[1].textContent, "cd");
  p.feed("ef\ngh\n");
  assert.deepStrictEqual(
    p.lines().map((l) => l.textContent), ["ab", "cdef", "gh"]);
});

test("without data-stream-filter no line is marked as a stream line", () => {
  const p = page({ showAll: true });
  p.feed('{"type":"assistant"}\nplain\n');
  assert.deepStrictEqual(p.lines().map((l) => l.className), ["ln", "ln"]);
});

test("with data-stream-filter only objects with a string type are stream lines", () => {
  const p = page({ filter: true, showAll: true });
  p.feed([
    '{"type":"assistant","n":1}',
    "plain text",
    '  {"type":"result"}',
    "[1,2]",
    '{"type":1}',
    "null",
    '{"type":"broken',
    '{"nope":true}',
    '\x1b[2m{"type":"assistant"}\x1b[0m',
    "",
  ].join("\n"));
  assert.deepStrictEqual(p.lines().map((l) => l.className), [
    "ln stream", "ln", "ln stream", "ln", "ln", "ln", "ln", "ln", "ln stream",
  ]);
});

test("the show-all checkbox toggles the log's show-all class", () => {
  const p = page({ filter: true, showAll: true });
  assert.ok(!p.log.classList.contains("show-all"));
  p.showAll.checked = true;
  p.showAll.dispatchEvent(new Event("change"));
  assert.ok(p.log.classList.contains("show-all"));
  p.showAll.checked = false;
  p.showAll.dispatchEvent(new Event("change"));
  assert.ok(!p.log.classList.contains("show-all"));
});

test("an unfinished stream line stays empty until show-all is checked", () => {
  const p = page({ filter: true, showAll: true });
  const partial = '{"type":"assistant","msg';
  p.feed(partial);
  assert.strictEqual(p.lines().length, 1);
  assert.strictEqual(p.lines()[0].className, "ln stream");
  assert.strictEqual(p.lines()[0].textContent, "");
  p.showAll.checked = true;
  p.showAll.dispatchEvent(new Event("change"));
  assert.strictEqual(p.lines()[0].textContent, partial);
  p.showAll.checked = false;
  p.showAll.dispatchEvent(new Event("change"));
  assert.strictEqual(p.lines()[0].textContent, "");
});

test("an unfinished stream line behind an ANSI prefix is still hidden", () => {
  const p = page({ filter: true, showAll: true });
  p.feed('\x1b[2m{"type":"assistant","msg');
  assert.strictEqual(p.lines()[0].className, "ln stream");
  assert.strictEqual(p.lines()[0].textContent, "");
});

test("an unfinished non-stream line is shown immediately", () => {
  const p = page({ filter: true, showAll: true });
  p.feed("building...");
  assert.strictEqual(p.lines()[0].className, "ln");
  assert.strictEqual(p.lines()[0].textContent, "building...");
});

test("a new pane follows the end", () => {
  const p = page();
  assert.strictEqual(p.followState.textContent, "following");
  assert.strictEqual(p.jump(), undefined);
});

test("scrolling away from the end pauses and offers a jump button", () => {
  const p = page();
  p.pause();
  assert.ok(p.followState.textContent.includes("paused"));
  assert.ok(p.jump());
  assert.strictEqual(p.jump().textContent, "jump to end");
});

test("new data while paused does not move the scroll position", () => {
  const p = page();
  p.pause();
  p.feed("more\n");
  assert.strictEqual(p.log.scrollTop, 0);
});

test("the jump button scrolls to the end and resumes following", () => {
  const p = page();
  p.pause();
  p.jump().dispatchEvent(new Event("click"));
  assert.strictEqual(p.log.scrollTop, 1000);
  assert.strictEqual(p.followState.textContent, "following");
  assert.strictEqual(p.jump(), undefined);
  p.log.scrollHeight = 2000;
  p.feed("more\n");
  assert.strictEqual(p.log.scrollTop, 2000);
});

test("while following, new data scrolls to the end", () => {
  const p = page();
  p.log.scrollHeight = 500;
  p.feed("a\n");
  assert.strictEqual(p.log.scrollTop, 500);
});

test("within 4px of the end counts as the bottom", () => {
  const p = page();
  p.log.scrollHeight = 1000;
  p.log.clientHeight = 100;
  p.log.scrollTop = 896;
  p.scrollEvent();
  assert.strictEqual(p.followState.textContent, "following");
  p.log.scrollTop = 895;
  p.scrollEvent();
  assert.ok(p.followState.textContent.includes("paused"));
  p.log.scrollTop = 900;
  p.scrollEvent();
  assert.strictEqual(p.followState.textContent, "following");
});

test("a scroll event on a hidden section is ignored", () => {
  const p = page();
  p.section.hidden = true;
  p.pause();
  assert.strictEqual(p.followState.textContent, "following");
});

test("a hidden section connects only when its tab is first clicked", () => {
  const p = page({ hidden: true, tab: true });
  assert.strictEqual(FakeEventSource.instances.length, 0);
  assert.ok(!p.tab.classList.contains("active"));
  p.tab.dispatchEvent(new Event("click"));
  assert.ok(p.tab.classList.contains("active"));
  assert.strictEqual(p.section.hidden, false);
  assert.strictEqual(FakeEventSource.instances.length, 1);
  assert.strictEqual(FakeEventSource.instances[0].url, "/log/stream");
  p.tab.dispatchEvent(new Event("click"));
  assert.strictEqual(FakeEventSource.instances.length, 1);
});

test("a frame over the time budget leaves the rest queued for the next frame", () => {
  // Each clock read costs 5ms: the 12ms budget admits two queued chunks a frame.
  const p = page({ clockStep: 5 });
  for (let i = 0; i < 5; i++) p.es.emit("log", "l" + i + "\n");
  assert.strictEqual(p.frames.length, 1);
  p.frames.shift()();
  assert.deepStrictEqual(p.lines().map((l) => l.textContent), ["l0", "l1"]);
  assert.strictEqual(p.frames.length, 1);
  p.drain();
  assert.deepStrictEqual(
    p.lines().map((l) => l.textContent), ["l0", "l1", "l2", "l3", "l4"]);
});

test("the stylesheet hides stream lines unless show-all is on", () => {
  assert.ok(STYLE.includes(".log:not(.show-all) .stream { display: none; }"));
});

test("a reconnect clears the log and the carried SGR state", () => {
  const p = page();
  p.feed("\x1b[31mold\npartial");
  assert.strictEqual(p.lines().length, 2);
  p.es.onopen();
  assert.strictEqual(p.lines().length, 0);
  p.feed("new\n");
  assert.deepStrictEqual(p.lines().map((l) => l.textContent), ["new"]);
  assert.deepStrictEqual(pieces(p.lines()[0]), ["new"]);
});

test("stream errors and pruning are reported next to the follow state", () => {
  const p = page();
  p.es.onerror();
  assert.strictEqual(p.followState.textContent, "reconnecting · following");
  p.es.onopen();
  assert.strictEqual(p.followState.textContent, "following");
  p.es.emit("pruned");
  assert.strictEqual(p.followState.textContent, "log pruned · following");
  assert.strictEqual(p.es.readyState, FakeEventSource.CLOSED);
  p.es.onerror();
  assert.strictEqual(p.followState.textContent, "log pruned · following");
});

test("a closed stream that errors reports the log as unavailable", () => {
  const p = page();
  p.es.readyState = FakeEventSource.CLOSED;
  p.es.onerror();
  assert.strictEqual(p.followState.textContent, "log unavailable · following");
});
