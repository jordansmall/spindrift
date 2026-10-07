(function () {
  "use strict";
  var status = document.getElementById("status");
  var history = document.getElementById("history");
  if (!status || !history || !window.EventSource) return;

  var limit = parseInt(history.dataset.limit, 10);

  // Matches the phase-pulse duration in style.css.
  var PULSE_MS = 900;
  // slot -> when its pulse started. Uptime ticks redraw the cards every frame,
  // so a pulse is re-applied, resumed mid-animation, until it has run out.
  var pulses = {};

  function phases() {
    var m = {};
    status.querySelectorAll("[data-slot]").forEach(function (c) {
      m[c.dataset.slot] = c.dataset.phase;
    });
    return m;
  }

  var es = new EventSource("/events");

  es.addEventListener("status", function (e) {
    var before = phases();
    status.innerHTML = e.data;
    var now = Date.now();
    status.querySelectorAll("[data-slot]").forEach(function (c) {
      var slot = c.dataset.slot;
      var was = before[slot];
      if (was !== undefined && was !== c.dataset.phase) pulses[slot] = now;
      var elapsed = now - pulses[slot];
      if (elapsed < PULSE_MS) {
        c.classList.add("phase-changed");
        c.style.animationDelay = -elapsed + "ms";
      } else {
        delete pulses[slot];
      }
    });
  });

  // Sent once per connection, so an EventSource auto-reconnect resyncs the list.
  es.addEventListener("history", function (e) {
    history.innerHTML = e.data;
  });

  es.addEventListener("entry", function (e) {
    var t = document.createElement("template");
    t.innerHTML = e.data.trim();
    var row = t.content.firstElementChild;
    if (!row) return;
    var empty = history.querySelector(".empty");
    if (empty) empty.remove();
    row.classList.add("fresh");
    history.insertBefore(row, history.querySelector(".entry"));
    var rows = history.querySelectorAll(".entry");
    for (var i = rows.length - 1; i >= limit; i--) rows[i].remove();
  });

  es.onopen = function () { document.body.classList.remove("disconnected"); };
  es.onerror = function () { document.body.classList.add("disconnected"); };
})();
