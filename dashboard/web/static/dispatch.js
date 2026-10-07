(function () {
  "use strict";

  // Log text is untrusted (issue comments can reach it): the DOM is built only
  // from createElement/createTextNode/style, never innerHTML (the one exception
  // is the server-rendered header frame, at the events listener below).

  // Fixed palette tuned for the dark theme; "black" is lifted so it stays legible.
  var PALETTE = [
    "#3d4556", "#e06c75", "#98c379", "#e5c07b", "#61afef", "#c678dd", "#56b6c2", "#c8ccd4",
    "#6b7488", "#ff8b94", "#b5e890", "#f5d98b", "#7ec3ff", "#e0a0ff", "#7fdbe6", "#ffffff"
  ];

  // CSI (any final byte), OSC (BEL or ESC \ terminated; an unterminated one
  // swallows the rest of the line), then truncated CSI and stray ESC.
  var ESCAPES = new RegExp(
    "\\u001b\\[([0-?]*)[ -/]*([@-~])" +
    "|\\u001b\\][^\\u0007\\u001b]*(?:\\u0007|\\u001b\\\\|$)" +
    "|\\u001b\\[[0-?]*[ -/]*$" +
    "|\\u001b", "g");

  function stripAnsi(s) { return s.replace(ESCAPES, ""); }

  function color256(n) {
    if (n < 16) return PALETTE[n];
    if (n >= 232) { var g = 8 + 10 * (n - 232); return "rgb(" + g + "," + g + "," + g + ")"; }
    n -= 16;
    var lv = function (v) { return v ? 55 + 40 * v : 0; };
    return "rgb(" + lv(Math.floor(n / 36)) + "," + lv(Math.floor(n / 6) % 6) + "," + lv(n % 6) + ")";
  }

  function rgb(r, g, b) {
    function c(v) { return Math.max(0, Math.min(255, v)); }
    return "rgb(" + c(r) + "," + c(g) + "," + c(b) + ")";
  }

  function newState() {
    return { bold: false, dim: false, italic: false, underline: false, fg: null, bg: null };
  }

  function cloneState(s) {
    return { bold: s.bold, dim: s.dim, italic: s.italic, underline: s.underline, fg: s.fg, bg: s.bg };
  }

  function plain(s) {
    return !(s.bold || s.dim || s.italic || s.underline || s.fg || s.bg);
  }

  // Applies the parameters of one SGR sequence to s in place.
  function applySGR(s, params) {
    var codes = params === "" ? [0] : params.split(";").map(function (p) {
      return p === "" ? 0 : parseInt(p, 10);
    });
    for (var i = 0; i < codes.length; i++) {
      var c = codes[i];
      if (c === 0) { var z = newState(); s.bold = z.bold; s.dim = z.dim; s.italic = z.italic; s.underline = z.underline; s.fg = z.fg; s.bg = z.bg; }
      else if (c === 1) s.bold = true;
      else if (c === 2) s.dim = true;
      else if (c === 3) s.italic = true;
      else if (c === 4) s.underline = true;
      else if (c === 22) { s.bold = false; s.dim = false; }
      else if (c === 23) s.italic = false;
      else if (c === 24) s.underline = false;
      else if (c >= 30 && c <= 37) s.fg = PALETTE[c - 30];
      else if (c >= 90 && c <= 97) s.fg = PALETTE[c - 90 + 8];
      else if (c >= 40 && c <= 47) s.bg = PALETTE[c - 40];
      else if (c >= 100 && c <= 107) s.bg = PALETTE[c - 100 + 8];
      else if (c === 39) s.fg = null;
      else if (c === 49) s.bg = null;
      else if (c === 38 || c === 48) {
        var col = null;
        if (codes[i + 1] === 5 && codes[i + 2] >= 0) { col = color256(Math.min(codes[i + 2], 255)); i += 2; }
        else if (codes[i + 1] === 2 && codes[i + 4] >= 0) { col = rgb(codes[i + 2], codes[i + 3], codes[i + 4]); i += 4; }
        else break; // malformed: the remaining parameters are unreliable
        if (c === 38) s.fg = col; else s.bg = col;
      }
    }
  }

  function appendText(parent, text, s) {
    if (text === "") return;
    var node = document.createTextNode(text);
    if (plain(s)) { parent.appendChild(node); return; }
    var span = document.createElement("span");
    if (s.bold) span.style.fontWeight = "bold";
    if (s.dim) span.style.opacity = "0.6";
    if (s.italic) span.style.fontStyle = "italic";
    if (s.underline) span.style.textDecoration = "underline";
    if (s.fg) span.style.color = s.fg;
    if (s.bg) span.style.backgroundColor = s.bg;
    span.appendChild(node);
    parent.appendChild(span);
  }

  // Renders text into parent, advancing s across every SGR sequence.
  function renderAnsi(parent, text, s) {
    var last = 0, m;
    ESCAPES.lastIndex = 0;
    while ((m = ESCAPES.exec(text)) !== null) {
      appendText(parent, text.slice(last, m.index), s);
      last = m.index + m[0].length;
      if (m[2] === "m") applySGR(s, m[1]);
    }
    appendText(parent, text.slice(last), s);
  }

  // A Box stream-JSON line: an object with a string "type".
  function isStreamLine(line) {
    var t = stripAnsi(line).replace(/^\s+/, "");
    if (t.charAt(0) !== "{") return false;
    try {
      var o = JSON.parse(t);
      return o !== null && typeof o === "object" && !Array.isArray(o) && typeof o.type === "string";
    } catch (e) {
      return false;
    }
  }

  // The unfinished last line cannot be parsed yet; guess from its opening so a
  // stream line stays hidden while it arrives.
  function looksStream(partial) {
    return /^\s*\{\s*"type"\s*:\s*"/.test(stripAnsi(partial.slice(0, 256)));
  }

  // One live follower per log pane.
  function follow(section) {
    var log = section.querySelector("pre.log");
    if (!log) return;
    var showAll = section.querySelector(".show-all");
    var followState = section.querySelector(".follow-state");
    // Pass logs are mostly stream-JSON, so only a pane that opts in hides it.
    var filter = log.hasAttribute("data-stream-filter");

    var state = newState(); // SGR state carried across committed lines
    var pending = "";       // the unfinished last line
    var pendingEl = null;   // its provisional element, always log's last child
    var queue = [];
    var qi = 0;
    var scheduled = false;
    var following = true;
    var note = "";
    var BUDGET_MS = 12;

    function lineEl(text) {
      var el = document.createElement("div");
      el.className = filter && isStreamLine(text) ? "ln stream" : "ln";
      renderAnsi(el, text, state);
      return el;
    }

    function renderPending() {
      if (pending === "") {
        if (pendingEl) { log.removeChild(pendingEl); pendingEl = null; }
        return;
      }
      if (!pendingEl) {
        pendingEl = document.createElement("div");
        log.appendChild(pendingEl);
      }
      var hidden = filter && looksStream(pending);
      pendingEl.className = hidden ? "ln stream" : "ln";
      pendingEl.textContent = "";
      // Rendering a multi-MB hidden line on every frame is wasted work.
      if (hidden && !(showAll && showAll.checked)) return;
      renderAnsi(pendingEl, pending.replace(/\r/g, ""), cloneState(state));
    }

    function atBottom() {
      return log.scrollHeight - log.scrollTop - log.clientHeight <= 4;
    }

    function scrollToEnd() {
      log.scrollTop = log.scrollHeight;
    }

    function updateIndicator() {
      if (!followState) return;
      followState.textContent = "";
      var parts = [];
      if (note) parts.push(note);
      if (following) {
        parts.push("following");
      } else {
        parts.push("paused");
      }
      followState.appendChild(
        document.createTextNode(parts.join(" · ") + (following ? "" : " · ")),
      );
      if (!following) {
        var jump = document.createElement("button");
        jump.type = "button";
        jump.className = "jump";
        jump.textContent = "jump to end";
        jump.addEventListener("click", function () {
          following = true;
          scrollToEnd();
          updateIndicator();
        });
        followState.appendChild(jump);
      }
    }

    function setFollowing(v) {
      if (v === following) return;
      following = v;
      updateIndicator();
    }

    // Drains the queue within a time budget per animation frame so a multi-MB
    // log arriving as 64 KiB frames neither janks the page nor thrashes layout.
    function flush() {
      scheduled = false;
      var frag = document.createDocumentFragment();
      var start = performance.now();
      while (qi < queue.length && performance.now() - start < BUDGET_MS) {
        var parts = (pending + queue[qi++]).split("\n");
        pending = parts.pop();
        for (var i = 0; i < parts.length; i++) {
          var line = parts[i].replace(/\r/g, "");
          frag.appendChild(lineEl(line));
        }
      }
      if (qi >= queue.length) { queue = []; qi = 0; }
      log.insertBefore(frag, pendingEl);
      renderPending();
      if (following) scrollToEnd();
      if (qi < queue.length) schedule();
    }

    function schedule() {
      if (scheduled) return;
      scheduled = true;
      requestAnimationFrame(flush);
    }

    function reset() {
      queue = [];
      qi = 0;
      pending = "";
      pendingEl = null;
      state = newState();
      log.textContent = "";
      following = true;
    }

    // A hidden pane has no layout: scroll metrics read as zero, so a scroll
    // event then must not be taken as the reader leaving the end.
    log.addEventListener("scroll", function () {
      if (section.hidden) return;
      setFollowing(atBottom());
    });

    if (showAll) {
      var applyShowAll = function () {
        log.classList.toggle("show-all", showAll.checked);
        renderPending();
        if (following) scrollToEnd();
      };
      showAll.addEventListener("change", applyShowAll);
      applyShowAll(); // the browser may restore the checkbox across a reload
    }

    // A pane streams only while its tab is shown: browsers cap HTTP/1.1 at
    // six connections per host, and a Dispatch with several fix passes would
    // otherwise stall the panes past the cap.
    var current = null;

    function connect() {
      var es = new EventSource(log.dataset.src);
      current = es;
      var opened = false;
      var done = false;

      es.onopen = function () {
        // The server re-sends the whole log after a reconnect.
        if (opened) reset();
        opened = true;
        note = "";
        updateIndicator();
      };

      es.addEventListener("log", function (e) {
        if (note) {
          note = "";
          updateIndicator();
        }
        queue.push(e.data);
        schedule();
      });

      es.addEventListener("waiting", function () {
        note = "waiting for log";
        updateIndicator();
      });

      es.addEventListener("pruned", function () {
        done = true;
        es.close();
        note = "log pruned";
        updateIndicator();
      });

      // A later Dispatch reused the fixed Pass log path; e.data is that path.
      // The ".prior-run.N" suffix mirrors quarantinePriorRunLogs in
      // cmd/launcher/internal/dispatch/box.go.
      es.addEventListener("superseded", function (e) {
        done = true;
        es.close();
        note = "log reused by a later run, kept as " + e.data + ".prior-run.N";
        updateIndicator();
      });

      es.onerror = function () {
        if (done) return;
        if (es.readyState === EventSource.CLOSED) {
          done = true;
          note = "log unavailable";
        } else {
          note = "reconnecting";
        }
        updateIndicator();
      };
    }

    function disconnect() {
      if (!current) return;
      current.close();
      current = null;
    }

    // The server replays the whole log on every connection, so a re-shown
    // pane starts over rather than appending to what it already holds.
    section.addEventListener("tabshown", function () {
      reset();
      note = "";
      updateIndicator();
      connect();
    });
    section.addEventListener("tabhidden", disconnect);

    if (!section.hidden) connect();
    updateIndicator();
  }

  // Arrays, not live NodeLists: Pass tabs added later join them.
  var tabs = Array.prototype.slice.call(document.querySelectorAll(".tab"));
  var bodies = Array.prototype.slice.call(document.querySelectorAll(".tabbody"));
  var haveES = !!window.EventSource;

  function showTab(name) {
    var i;
    for (i = 0; i < tabs.length; i++) {
      tabs[i].classList.toggle("active", tabs[i].getAttribute("data-tab") === name);
    }
    // Every tabhidden first, so the old stream closes before the new one
    // opens whatever the tabs' page order.
    var shown = [];
    for (i = 0; i < bodies.length; i++) {
      var show = bodies[i].id === "tab-" + name;
      var wasHidden = bodies[i].hidden;
      bodies[i].hidden = !show;
      if (show && wasHidden) shown.push(bodies[i]);
      if (!show && !wasHidden) bodies[i].dispatchEvent(new Event("tabhidden"));
    }
    for (i = 0; i < shown.length; i++) shown[i].dispatchEvent(new Event("tabshown"));
  }

  function wireTab(tab) {
    tab.addEventListener("click", function () {
      showTab(this.getAttribute("data-tab"));
    });
  }

  tabs.forEach(wireTab);

  // Without EventSource only the live streaming is lost; tabs still switch.
  if (haveES) bodies.forEach(follow);

  function addPass(phase, path) {
    var nav = document.querySelector("nav.tabs");
    var last = bodies[bodies.length - 1];
    if (!nav || !last) return;
    var k = 0;
    for (var i = 0; i < bodies.length; i++) {
      if (!bodies[i].hasAttribute("data-pass-log")) continue;
      if (bodies[i].getAttribute("data-pass-log") === path) return;
      k++;
    }

    var tab = document.createElement("button");
    tab.setAttribute("type", "button");
    tab.className = "tab";
    tab.setAttribute("data-tab", "pass-" + k);
    tab.textContent = phase;
    nav.appendChild(tab);
    wireTab(tab);
    tabs.push(tab);

    var pane = document.createElement("section");
    pane.className = "tabbody";
    pane.id = "tab-pass-" + k;
    pane.setAttribute("data-pass-log", path);
    pane.hidden = true;
    var bar = document.createElement("div");
    bar.className = "logbar";
    var state = document.createElement("span");
    state.className = "follow-state";
    bar.appendChild(state);
    var pre = document.createElement("pre");
    pre.className = "log";
    // /log needs this Dispatch's slot, at and n so a later run's reuse of
    // the path is not followed; each is re-escaped, never copied raw from
    // the page.
    pre.dataset.src = "/log?path=" + encodeURIComponent(path) +
      "&slot=" + encodeURIComponent(eventsParam("slot")) +
      "&at=" + encodeURIComponent(eventsParam("at")) +
      "&n=" + encodeURIComponent(eventsParam("n"));
    pane.appendChild(bar);
    pane.appendChild(pre);
    last.parentNode.insertBefore(pane, last.nextSibling);
    bodies.push(pane);
    // follow() streams the pane only while it is shown, which keeps the
    // browser under its per-host connection cap.
    follow(pane);
  }

  function eventsParam(name) {
    var q = eventsURL.slice(eventsURL.indexOf("?") + 1).split("&");
    for (var i = 0; i < q.length; i++) {
      var eq = q[i].indexOf("=");
      if (q[i].slice(0, eq) === name) return decodeURIComponent(q[i].slice(eq + 1));
    }
    return "";
  }

  var main = document.querySelector("main.dispatch");
  var eventsURL = main && main.getAttribute("data-events");
  if (eventsURL && haveES) {
    var events = new EventSource(eventsURL);
    // Reconnects replay every Pass; the path dedupe absorbs the repeats.
    events.addEventListener("pass", function (ev) {
      var p;
      try { p = JSON.parse(ev.data); } catch (e) { return; }
      if (!p || typeof p.phase !== "string" || typeof p.path !== "string") return;
      addPass(p.phase, p.path);
    });
    // Deliberate, narrow innerHTML exception: the frame is server-rendered and
    // template-escaped, the same trust live.js extends to its swaps.
    var localize = window.localizeTimes || function () {};
    events.addEventListener("head", function (ev) {
      var head = main.querySelector("header.dispatch-head");
      if (!head) return;
      head.innerHTML = ev.data;
      localize(head);
    });
    events.addEventListener("closed", function () { events.close(); });
  }
})();
