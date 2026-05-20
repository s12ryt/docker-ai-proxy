// Landing page lightweight stats: pulls /healthz (public) and falls back to
// localStorage-stored admin token (if user opened dashboard once).
(function () {
  document.getElementById("year").textContent = new Date().getFullYear();

  const adminToken = localStorage.getItem("ai-hub-admin-token");
  if (!adminToken) return;

  fetch("/api/summary?hours=24", { headers: { Authorization: "Bearer " + adminToken } })
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

  fetch("/api/runtime", { headers: { Authorization: "Bearer " + adminToken } })
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
