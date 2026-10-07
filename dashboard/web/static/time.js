(function () {
  "use strict";
  // timeStyle "long" names the zone, so a viewer can tell it is not UTC.
  var FORMAT = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "long" });

  // Strict RFC3339: `new Date` alone reads "1" as a year and a zone-less
  // time as local, which would rewrite a malformed value into a wrong one.
  var RFC3339 = /^\d{4}-\d{2}-\d{2}[Tt]\d{2}:\d{2}:\d{2}(\.\d+)?([Zz]|[+-]\d{2}:\d{2})$/;

  // Reads the datetime attribute, never the text, so a repeat call is harmless.
  window.localizeTimes = function (root) {
    root.querySelectorAll("time[datetime]").forEach(function (el) {
      var raw = el.getAttribute("datetime");
      if (!RFC3339.test(raw)) return;
      var d = new Date(raw);
      if (isNaN(d.getTime())) return;
      el.title = raw;
      el.textContent = FORMAT.format(d);
    });
  };

  window.localizeTimes(document);
})();
