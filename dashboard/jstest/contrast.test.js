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

// --name -> #hex for the hex-valued custom properties of one `:root { ... }`
// block (selector is the text before the brace, e.g. a media-query body).
function rootVars(css, nth) {
  const blocks = [...css.matchAll(/:root\s*\{([^}]*)\}/g)];
  assert.ok(blocks[nth], `no :root block #${nth} in style.css`);
  const vars = {};
  for (const m of blocks[nth][1].matchAll(/(--[\w-]+)\s*:\s*(#[0-9a-fA-F]{6})\s*;/g)) vars[m[1]] = m[2].toLowerCase();
  return vars;
}

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

const STATES = [
  "awaiting_window", "resolving", "running", "backing_off", "ci-wait", "image-build",
  "working", "waiting", "jammed", "asleep", "checking", "halted",
];
const TEXT = 4.5;
const NON_TEXT = 3;

// [fg, bg, minimum ratio, what it is]; "--x" names a :root var, "ansi:N" a PALETTE entry.
const PAIRS = [];
for (const bg of ["--bg", "--panel"]) {
  for (const fg of ["--text", "--muted", "--idle", ...STATES.map((s) => `--${s}`)]) {
    PAIRS.push([fg, bg, TEXT, "text"]);
  }
}
PAIRS.push(["--muted", "--bg", NON_TEXT, "active-tab indicator (.tab.active border-bottom-color)"]);
PAIRS.push(["--log-fg", "--log-bg", TEXT, "log text"]);
for (let i = 0; i < 16; i++) PAIRS.push([`ansi:${i}`, "--log-bg", TEXT, `ANSI colour ${i} on the log pane`]);

const EXEMPT = {
  "--border": "decorative card border; pill borders use currentColor",
  "--log-border": "decorative log pane border",
};

function themeFailures(vars, pal) {
  const resolve = (ref) => (ref.startsWith("ansi:") ? pal[Number(ref.slice(5))] : vars[ref]);
  const failures = [];
  for (const [fg, bg, min, what] of PAIRS) {
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
  assert.deepStrictEqual(themeFailures(rootVars(STYLE, 0), palette(SOURCE)), []);
});

test("every :root colour is in the contrast table or exempt", () => {
  const listed = new Set(Object.keys(EXEMPT));
  for (const [fg, bg] of PAIRS) [fg, bg].forEach((r) => listed.add(r));
  const unlisted = Object.keys(rootVars(STYLE, 0)).filter((v) => !listed.has(v));
  assert.deepStrictEqual(unlisted, [], "add these to PAIRS or EXEMPT in contrast.test.js");
});

test("style.css declares no opacity", () => {
  const found = STYLE.split("\n").filter((l) => /(^|[\s{;])opacity\s*:/.test(l));
  assert.deepStrictEqual(found, [], "opacity blends text into its background, so the contrast table cannot vouch for it");
});
