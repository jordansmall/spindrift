"use strict";

// WCAG 2.1 contrast of the Dashboard's declared colours, read straight from
// style.css and dispatch.js. Node built-ins only (ADR 0060).

const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "web", "static");
const SOURCE = fs.readFileSync(path.join(STATIC, "dispatch.js"), "utf8");
const STYLE = fs.readFileSync(path.join(STATIC, "style.css"), "utf8");

// Custom-property declarations of a CSS block body: [[--name, value], ...].
function declarations(body) {
  return [...body.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)].map((m) => [m[1], m[2].trim()]);
}

// Neither :root body contains a nested brace, so `[^}]*` ends at the right `}`.
function rootBlock(css) {
  const m = /(?:^|\n):root\s*\{([^}]*)\}/.exec(css);
  assert.ok(m, "no top-level :root block in style.css");
  return m[1];
}

function lightBlock(css) {
  const open = /@media\s*\(prefers-color-scheme:\s*light\)\s*\{/.exec(css);
  assert.ok(open, "no `@media (prefers-color-scheme: light)` block in style.css");
  const start = open.index + open[0].length;
  let depth = 1;
  let end = start;
  while (end < css.length && depth > 0) {
    if (css[end] === "{") depth++;
    else if (css[end] === "}") depth--;
    end++;
  }
  const m = /:root\s*\{([^}]*)\}/.exec(css.slice(start, end));
  assert.ok(m, "no `:root { ... }` rule inside the `@media (prefers-color-scheme: light)` block in style.css");
  return m[1];
}

function colours(body) {
  const vars = {};
  for (const [name, value] of declarations(body)) {
    assert.match(value, /^#[0-9a-fA-F]{6}$/, `${name}: ${value} -- colours must be #rrggbb so the contrast test can read them`);
    vars[name] = value.toLowerCase();
  }
  return vars;
}

const darkVars = () => colours(rootBlock(STYLE));
// The light theme is the dark vars with the light block laid over them.
const lightVars = () => ({ ...darkVars(), ...colours(lightBlock(STYLE)) });

function palette(js) {
  const m = /var PALETTE = \[([^\]]*)\]/.exec(js);
  assert.ok(m, "PALETTE not found in dispatch.js");
  return [...m[1].matchAll(/"(#[0-9a-fA-F]{6})"/g)].map((x) => x[1].toLowerCase());
}

function luminance(hex) {
  const [r, g, b] = [1, 3, 5].map((i) => {
    const c = parseInt(hex.slice(i, i + 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrast(a, b) {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

const TEXT = 4.5;
const NON_TEXT = 3;

const EXEMPT = {
  "--border": "decorative card border; pill borders use currentColor",
  "--log-border": "decorative log pane border",
};

// [fg, bg, minimum ratio, what it is]; "--x" names a :root var, "ansi:N" a PALETTE entry.
// Any :root colour that is not a surface, a log var or exempt is text, so a
// new state in style.css is covered with no edit here.
function pairs() {
  const surfaces = ["--bg", "--panel"];
  const texts = Object.keys(darkVars()).filter(
    (v) => !surfaces.includes(v) && !v.startsWith("--log-") && !(v in EXEMPT),
  );
  const out = [];
  for (const bg of surfaces) for (const fg of texts) out.push([fg, bg, TEXT, "text"]);
  out.push(["--muted", "--bg", NON_TEXT, "active-tab indicator (.tab.active border-bottom-color)"]);
  out.push(["--log-fg", "--log-bg", TEXT, "log text"]);
  for (let i = 0; i < 16; i++) out.push([`ansi:${i}`, "--log-bg", TEXT, `ANSI colour ${i} on the log pane`]);
  return out;
}

function themeFailures(vars, pal) {
  const resolve = (ref) => (ref.startsWith("ansi:") ? pal[Number(ref.slice(5))] : vars[ref]);
  const failures = [];
  for (const [fg, bg, min, what] of pairs()) {
    const [f, b] = [resolve(fg), resolve(bg)];
    if (!f || !b) {
      failures.push(`${fg} on ${bg}: colour missing (${what})`);
      continue;
    }
    const ratio = contrast(f, b);
    if (ratio < min) failures.push(`${fg} ${f} on ${bg} ${b}: ${ratio.toFixed(2)} < ${min} (${what})`);
  }
  return failures;
}

test("PALETTE has the 16 ANSI colours", () => {
  assert.strictEqual(palette(SOURCE).length, 16);
});

test("dark theme and log pane meet WCAG AA", () => {
  assert.deepStrictEqual(themeFailures(darkVars(), palette(SOURCE)), []);
});

test("light theme and log pane meet WCAG AA", () => {
  assert.deepStrictEqual(themeFailures(lightVars(), palette(SOURCE)), []);
});

test("the log pane is dark in both themes", () => {
  const overridden = Object.keys(colours(lightBlock(STYLE))).filter((v) => v.startsWith("--log-"));
  assert.deepStrictEqual(overridden, [], "the light block must not touch --log-*");
});

test("the log pane colours equal the dark --bg, --text and --border", () => {
  const v = darkVars();
  assert.deepStrictEqual(
    [v["--log-bg"], v["--log-fg"], v["--log-border"]],
    [v["--bg"], v["--text"], v["--border"]],
  );
});

test("the light block overrides every non-log colour", () => {
  const light = new Set(Object.keys(colours(lightBlock(STYLE))));
  const leaked = Object.keys(darkVars()).filter((v) => !v.startsWith("--log-") && !light.has(v));
  assert.deepStrictEqual(leaked, [], "these dark colours would leak into light mode");
});

test("every :root colour is in the contrast table or exempt", () => {
  const listed = new Set(Object.keys(EXEMPT));
  for (const [fg, bg] of pairs()) [fg, bg].forEach((r) => listed.add(r));
  const all = [...declarations(rootBlock(STYLE)), ...declarations(lightBlock(STYLE))].map(([n]) => n);
  const unlisted = all.filter((v) => !listed.has(v));
  assert.deepStrictEqual(unlisted, [], "add these to PAIRS or EXEMPT in contrast.test.js");
});

test(":root declares color-scheme: light dark", () => {
  assert.match(rootBlock(STYLE), /color-scheme:\s*light dark\s*;/);
});

test("the log pane declares color-scheme: dark", () => {
  // Without it, an OS light preference gives the dark pane a light scrollbar.
  const m = /^\.log\s*\{([^}]*)\}/m.exec(STYLE);
  assert.ok(m, "no `.log { ... }` rule in style.css");
  assert.match(m[1], /color-scheme:\s*dark\s*;/);
});

test("style.css declares no opacity", () => {
  const found = STYLE.split("\n").filter((l) => /(^|[\s{;])opacity\s*:/.test(l));
  assert.deepStrictEqual(found, [], "opacity blends text into its background, so the contrast table cannot vouch for it");
});
