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

  get nextSibling() {
    if (!this.parentNode) return null;
    const sibs = this.parentNode.childNodes;
    return sibs[sibs.indexOf(this) + 1] || null;
  }

  get children() { return this.childNodes.filter((n) => n.nodeType === 1); }

  get textContent() { return this.childNodes.map((n) => n.textContent).join(""); }
  set textContent(v) {
    this.childNodes.forEach((n) => { n.parentNode = null; });
    this.childNodes = [];
    if (v !== "") this.appendChild(new Text(String(v)));
  }

  // dispatch.js builds log DOM from createElement/createTextNode only (see its
  // untrusted-log header comment), so an innerHTML write is a test failure;
  // the one server-rendered header swap overrides this on its own element.
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
    FakeEventSource.openAtConnect.push(
      FakeEventSource.instances.filter((e) => e.readyState !== FakeEventSource.CLOSED).length + 1);
    FakeEventSource.instances.push(this);
  }
  addEventListener(type, fn) {
    (this.listeners[type] = this.listeners[type] || []).push(fn);
  }
  emit(type, data) {
    if (this.readyState === FakeEventSource.CLOSED) return;
    (this.listeners[type] || []).forEach((fn) => fn({ type, data }));
  }
  close() { this.readyState = FakeEventSource.CLOSED; }
}
FakeEventSource.CLOSED = 2;
FakeEventSource.openAtConnect = [];
FakeEventSource.instances = [];

// One .tabbody pane: its optional tab button, log, optional show-all box and
// follow-state note. Pane `name` streams /log/<name> unless o.src says otherwise.
function makePane(body, o) {
  let tab = null;
  if (o.tab) {
    tab = new Element("button");
    tab.className = "tab";
    tab.setAttribute("data-tab", o.name);
    body.appendChild(tab);
  }
  const section = new Element("section");
  section.className = "tabbody";
  section.id = "tab-" + o.name;
  section.hidden = !!o.hidden;
  const log = new Element("pre");
  log.className = "log";
  log.dataset.src = o.src;
  if (o.filter) log.setAttribute("data-stream-filter", "");
  section.appendChild(log);
  let showAll = null;
  if (o.showAll) {
    showAll = new Element("input");
    showAll.className = "show-all";
    section.appendChild(showAll);
  }
  const followState = new Element("span");
  followState.className = "follow-state";
  section.appendChild(followState);
  body.appendChild(section);
  return { tab, section, log, showAll, followState };
}

// Runs dispatch.js against body's DOM; frames queue until drain() runs them.
function runPage(body, clockStep, windowExtras) {
  const frames = [];
  let clock = 0;
  FakeEventSource.instances = [];
  const sandbox = {
    document: {
      createElement: (tag) => new Element(tag),
      createTextNode: (data) => new Text(data),
      createDocumentFragment: () => new Container(11),
      querySelectorAll: (sel) => body.querySelectorAll(sel),
      querySelector: (sel) => body.querySelector(sel),
    },
    window: Object.assign({ EventSource: FakeEventSource }, windowExtras),
    EventSource: FakeEventSource,
    Event,
    // Each read advances the clock by clockStep ms; the default 0 never
    // trips the 12ms per-frame budget, so one frame drains everything.
    performance: { now: () => { const t = clock; clock += clockStep || 0; return t; } },
    requestAnimationFrame: (fn) => { frames.push(fn); },
  };
  vm.runInNewContext(SOURCE, sandbox);
  return { frames, drain() { while (frames.length) frames.shift()(); } };
}

// Scrolls a log away from its end, which pauses following.
function pauseLog(log) {
  log.scrollHeight = 1000;
  log.clientHeight = 100;
  log.scrollTop = 0;
  log.dispatchEvent(new Event("scroll"));
}

// Builds one .tabbody pane, runs dispatch.js against it, and opens the stream.
function page(opts) {
  opts = opts || {};
  const body = new Element("body");
  const { tab, section, log, showAll, followState } = makePane(body, {
    name: "log",
    src: "/log/stream",
    tab: opts.tab,
    hidden: opts.hidden,
    filter: opts.filter,
    showAll: opts.showAll,
  });
  const { frames, drain } = runPage(body, opts.clockStep);

  const es = FakeEventSource.instances[0];
  if (es) es.onopen();
  return {
    section, log, showAll, followState, es, tab, frames, drain,
    feed(text) { es.emit("log", text); drain(); },
    lines() { return log.children; },
    jump() { return followState.children.find((c) => c.className === "jump"); },
    scrollEvent() { log.dispatchEvent(new Event("scroll")); },
    pause() { pauseLog(log); },
  };
}

// n log panes with a tab each, the first one shown: how a Dispatch with
// Pass logs lays out. Pane i streams /log/i.
function tabsPage(n) {
  const body = new Element("body");
  const panes = [];
  for (let i = 0; i < n; i++) {
    panes.push(makePane(body, { name: "t" + i, src: "/log/" + i, tab: true, hidden: i !== 0 }));
  }
  const { drain } = runPage(body);
  return {
    panes,
    show(i) { panes[i].tab.dispatchEvent(new Event("click")); },
    drain,
    open() {
      return FakeEventSource.instances.filter((e) => e.readyState !== FakeEventSource.CLOSED);
    },
    latest() { return FakeEventSource.instances[FakeEventSource.instances.length - 1]; },
    texts(i) { return panes[i].log.children.map((l) => l.textContent); },
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

test("a hidden section connects when its tab is clicked, and clicking the active tab does nothing", () => {
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

test("an opened frame clears the waiting note without adding content", () => {
  const p = page();
  p.es.emit("waiting");
  assert.strictEqual(p.followState.textContent, "waiting for log · following");
  p.es.emit("opened", "issue-7.log");
  assert.strictEqual(p.followState.textContent, "following");
  assert.strictEqual(p.lines().length, 0);
});

test("a superseded log closes the stream and names the kept copy", () => {
  const p = page();
  p.es.emit("superseded", "issue-7.log");
  assert.strictEqual(
    p.followState.textContent,
    "log reused by a later run, kept as issue-7.log.prior-run.N · following",
  );
  assert.strictEqual(p.es.readyState, FakeEventSource.CLOSED);
  p.es.onerror();
  assert.ok(p.followState.textContent.startsWith("log reused by a later run"));
});

test("a closed stream that errors reports the log as unavailable", () => {
  const p = page();
  p.es.readyState = FakeEventSource.CLOSED;
  p.es.onerror();
  assert.strictEqual(p.followState.textContent, "log unavailable · following");
});

test("hiding a tab closes its stream and showing it opens a new one", () => {
  const p = tabsPage(2);
  const first = p.latest();
  assert.strictEqual(FakeEventSource.instances.length, 1);
  p.show(1);
  assert.strictEqual(first.readyState, FakeEventSource.CLOSED);
  assert.strictEqual(FakeEventSource.instances.length, 2);
  assert.strictEqual(p.open().length, 1);
  p.show(0);
  assert.strictEqual(FakeEventSource.instances.length, 3);
  assert.strictEqual(p.latest().url, "/log/0");
  assert.notStrictEqual(p.latest(), first);
  assert.deepStrictEqual(p.open(), [p.latest()]);
});

test("switching back to an earlier tab never has two streams open at once", () => {
  const p = tabsPage(3);
  p.show(2);
  FakeEventSource.openAtConnect = [];
  p.show(0);
  assert.deepStrictEqual(FakeEventSource.openAtConnect, [1]);
  assert.strictEqual(p.open().length, 1);
});

test("showing a tab again replays its log without duplicated lines", () => {
  const p = tabsPage(2);
  const es1 = p.latest();
  es1.onopen();
  es1.emit("log", "a\nb\n");
  p.drain();
  assert.deepStrictEqual(p.texts(0), ["a", "b"]);
  p.show(1);
  p.show(0);
  const es2 = p.latest();
  es2.onopen();
  es2.emit("log", "a\nb\n");
  p.drain();
  assert.deepStrictEqual(p.texts(0), ["a", "b"]);
});

test("showing a tab again follows the end and drops stale notes", () => {
  const p = tabsPage(2);
  const es1 = p.latest();
  es1.onopen();
  pauseLog(p.panes[0].log);
  es1.emit("pruned");
  assert.ok(p.panes[0].followState.textContent.startsWith("log pruned · paused"));
  p.show(1);
  p.show(0);
  assert.strictEqual(p.panes[0].followState.textContent, "following");
});

test("cycling through many tabs never holds more than one stream open", () => {
  const p = tabsPage(8);
  assert.deepStrictEqual(p.open().map((e) => e.url), ["/log/0"]);
  for (let i = 1; i < 8; i++) {
    p.show(i);
    assert.deepStrictEqual(p.open().map((e) => e.url), ["/log/" + i]);
  }
  for (let i = 0; i < 8; i++) {
    p.show(i);
    assert.strictEqual(p.open().length, 1);
    assert.strictEqual(p.latest().url, "/log/" + i);
    p.open()[0].emit("log", "line " + i + "\n");
    p.drain();
    assert.deepStrictEqual(p.texts(i), ["line " + i]);
  }
});

test("a tab hidden and shown again starts without the carried SGR state", () => {
  const p = tabsPage(2);
  const es1 = p.latest();
  es1.onopen();
  es1.emit("log", "\x1b[31mred\n");
  p.drain();
  assert.ok(pieces(p.panes[0].log.children[0]).some((x) => typeof x === "object"));
  p.show(1);
  p.show(0);
  const es2 = p.latest();
  es2.onopen();
  es2.emit("log", "plain\n");
  p.drain();
  assert.deepStrictEqual(p.texts(0), ["plain"]);
  assert.deepStrictEqual(pieces(p.panes[0].log.children[0]), ["plain"]);
});

test("a pruned tab answers pruned again when shown again", () => {
  const p = tabsPage(2);
  p.latest().emit("pruned");
  p.show(1);
  p.show(0);
  p.latest().emit("pruned");
  assert.strictEqual(p.panes[0].followState.textContent, "log pruned · following");
  assert.strictEqual(p.latest().readyState, FakeEventSource.CLOSED);
});

// A drill-in page: tabs and Pass panes as dispatch.html.tmpl renders them,
// with the dispatch events stream opened when events is given.
function dispatchPage(opts) {
  const body = new Element("body");
  const main = new Element("main");
  main.className = "dispatch";
  if (opts.events) main.setAttribute("data-events", opts.events);
  body.appendChild(main);
  const head = new Element("header");
  head.className = "dispatch-head";
  const headWrites = [];
  Object.defineProperty(head, "innerHTML", {
    set(v) { headWrites.push(v); },
  });
  main.appendChild(head);
  const nav = new Element("nav");
  nav.className = "tabs";
  main.appendChild(nav);
  (opts.passes || []).forEach((q, i) => {
    const tab = new Element("button");
    tab.className = "tab";
    tab.setAttribute("data-tab", "pass-" + i);
    tab.textContent = q.phase;
    nav.appendChild(tab);
    const sec = new Element("section");
    sec.className = "tabbody";
    sec.id = "tab-pass-" + i;
    sec.setAttribute("data-pass-log", q.path);
    sec.hidden = true;
    const pre = new Element("pre");
    pre.className = "log";
    pre.dataset.src = "/log?path=" + q.path;
    sec.appendChild(pre);
    main.appendChild(sec);
  });

  const localized = [];
  runPage(body, 0, opts.localize === false ? {} : { localizeTimes: (el) => localized.push(el) });
  const events = FakeEventSource.instances.find((e) => e.url === opts.events);
  return {
    main, events, head, headWrites, localized,
    tabs: () => nav.children,
    panes: () => main.querySelectorAll(".tabbody"),
    logStreams: () => FakeEventSource.instances.filter((e) => e.url.startsWith("/log")),
    pass(phase, logPath) { events.emit("pass", JSON.stringify({ phase, path: logPath })); },
  };
}

const EVENTS = "/dispatch/events?slot=s&at=a&n=1";

test("a new pass frame adds a tab and a hidden pane after the existing ones", () => {
  const p = dispatchPage({ events: EVENTS, passes: [{ phase: "plan", path: "/l/0" }] });
  p.pass("implement", "/l/a b&c");
  assert.strictEqual(p.tabs().length, 2);
  const tab = p.tabs()[1];
  assert.strictEqual(tab.textContent, "implement");
  assert.strictEqual(tab.getAttribute("data-tab"), "pass-1");
  assert.ok(tab.classList.contains("tab"));
  const panes = p.panes();
  assert.strictEqual(panes.length, 2);
  assert.strictEqual(p.main.children[p.main.children.length - 1], panes[1]);
  assert.strictEqual(panes[1].id, "tab-pass-1");
  assert.strictEqual(panes[1].hidden, true);
  assert.strictEqual(panes[1].getAttribute("data-pass-log"), "/l/a b&c");
  assert.strictEqual(
    panes[1].querySelector("pre.log").dataset.src, "/log?path=" + encodeURIComponent("/l/a b&c") + "&slot=s&at=a&n=1");
});

test("a live pane opens its log stream only when its tab is first clicked", () => {
  const p = dispatchPage({ events: EVENTS, passes: [{ phase: "plan", path: "/l/0" }] });
  p.pass("implement", "/l/1");
  assert.strictEqual(p.logStreams().length, 0);
  p.tabs()[1].dispatchEvent(new Event("click"));
  assert.strictEqual(p.panes()[1].hidden, false);
  assert.ok(p.tabs()[1].classList.contains("active"));
  assert.strictEqual(p.panes()[0].hidden, true);
  assert.strictEqual(p.logStreams().length, 1);
  assert.strictEqual(p.logStreams()[0].url, "/log?path=" + encodeURIComponent("/l/1") + "&slot=s&at=a&n=1");
});

test("a pass frame for a path already on the page adds nothing", () => {
  const p = dispatchPage({ events: EVENTS, passes: [{ phase: "plan", path: "/l/0" }] });
  p.pass("plan", "/l/0");
  p.pass("review", "/l/1");
  p.pass("review", "/l/1");
  assert.strictEqual(p.tabs().length, 2);
  assert.strictEqual(p.panes().length, 2);
});

test("a live pane's log src re-escapes the events URL's slot, at and n", () => {
  const p = dispatchPage({
    events: "/dispatch/events?slot=a%22b&at=2026-10-07T19%3A20%3A00%2B02%3A00&n=1&x=%3Cy%3E",
    passes: [{ phase: "plan", path: "/l/0" }],
  });
  p.pass("implement", "/l/1");
  assert.strictEqual(
    p.panes()[1].querySelector("pre.log").dataset.src,
    "/log?path=%2Fl%2F1&slot=a%22b&at=2026-10-07T19%3A20%3A00%2B02%3A00&n=1");
});

test("a hostile phase lands as text", () => {
  const p = dispatchPage({ events: EVENTS, passes: [{ phase: "plan", path: "/l/0" }] });
  p.pass("<img src=x onerror=alert(1)>", "/l/1");
  const tab = p.tabs()[1];
  assert.strictEqual(tab.textContent, "<img src=x onerror=alert(1)>");
  assert.strictEqual(tab.children.length, 0);
});

test("a closed frame closes the events stream", () => {
  const p = dispatchPage({ events: EVENTS });
  assert.notStrictEqual(p.events.readyState, FakeEventSource.CLOSED);
  p.events.emit("closed", "");
  assert.strictEqual(p.events.readyState, FakeEventSource.CLOSED);
});

test("a head frame replaces the header's contents and localizes its times", () => {
  const p = dispatchPage({ events: EVENTS });
  p.events.emit("head", "<h1>x</h1><time>t</time>");
  assert.deepStrictEqual(p.headWrites, ["<h1>x</h1><time>t</time>"]);
  assert.deepStrictEqual(p.localized, [p.head]);
  p.events.emit("head", "<h1>y</h1>");
  assert.deepStrictEqual(p.headWrites, ["<h1>x</h1><time>t</time>", "<h1>y</h1>"]);
  assert.strictEqual(p.localized.length, 2);
});

test("a head frame still applies without window.localizeTimes", () => {
  const p = dispatchPage({ events: EVENTS, localize: false });
  p.events.emit("head", "<h1>x</h1>");
  assert.deepStrictEqual(p.headWrites, ["<h1>x</h1>"]);
});

test("a page without data-events opens no dispatch events stream", () => {
  dispatchPage({ passes: [{ phase: "plan", path: "/l/0" }] });
  assert.strictEqual(FakeEventSource.instances.length, 0);
});
