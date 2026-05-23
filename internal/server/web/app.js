// Landing page lightweight stats: uses the HttpOnly dashboard session cookie when present.
(function () {
  document.getElementById("year").textContent = new Date().getFullYear();

  fetch("/api/summary?hours=24", { credentials: "same-origin" })
    .then((r) => (r.ok ? r.json() : null))
    .then((s) => {
      if (!s) return;
      document.getElementById("stat-rps").textContent = s.total_calls.toLocaleString();
      document.getElementById("stat-latency").textContent =
        Math.round(s.avg_latency_ms) + " ms";
      document.getElementById("stat-providers").textContent =
        Object.keys(s.providers || {}).length || "—";
    })
    .catch(() => {});

  fetch("/api/runtime", { credentials: "same-origin" })
    .then((r) => (r.ok ? r.json() : null))
    .then((r) => {
      if (!r) return;
      document.getElementById("stat-uptime").textContent = r.uptime || "—";
      if (!document.getElementById("stat-providers").textContent.match(/\d/)) {
        document.getElementById("stat-providers").textContent = r.providers;
      }
    })
    .catch(() => {});
})();
