(function () {
  const $ = (id) => document.getElementById(id);
  const tokenInput = $("admin-token");
  const hoursSel = $("hours");
  const refreshBtn = $("refresh");

  const stored = localStorage.getItem("ai-hub-admin-token");
  if (stored) tokenInput.value = stored;

  $("signout").addEventListener("click", (e) => {
    e.preventDefault();
    localStorage.removeItem("ai-hub-admin-token");
    tokenInput.value = "";
    location.reload();
  });

  function token() {
    const t = tokenInput.value.trim();
    if (t) localStorage.setItem("ai-hub-admin-token", t);
    return t;
  }

  async function fetchJSON(path) {
    const t = token();
    if (!t) throw new Error("missing admin token");
    const r = await fetch(path, { headers: { Authorization: "Bearer " + t } });
    if (!r.ok) throw new Error(`${path}: ${r.status}`);
    return r.json();
  }

  function fmt(n) {
    if (n == null) return "—";
    if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + "M";
    if (n >= 1_000) return (n / 1_000).toFixed(1) + "k";
    return Math.round(n).toLocaleString();
  }

  function fmtTime(ms) {
    const d = new Date(ms);
    return d.toLocaleString();
  }

  function renderSummary(s) {
    $("kpi-calls").textContent = fmt(s.total_calls);
    $("kpi-window").textContent = `${s.window_hours} 小時窗口`;
    $("kpi-errors").textContent = fmt(s.total_errors);
    const rate = s.total_calls > 0 ? ((s.total_errors / s.total_calls) * 100).toFixed(2) : "0.00";
    $("kpi-rate").textContent = `錯誤率 ${rate}%`;
    $("kpi-latency").textContent = Math.round(s.avg_latency_ms);
    $("kpi-tokens").textContent = fmt((s.tokens_in || 0) + (s.tokens_out || 0));
    $("kpi-tokens-sub").textContent = `${fmt(s.tokens_in)} / ${fmt(s.tokens_out)}`;

    const list = $("bar-list");
    list.innerHTML = "";
    const entries = Object.entries(s.providers || {});
    if (!entries.length) {
      list.innerHTML = '<div class="empty">尚無流量資料</div>';
      return;
    }
    const max = Math.max(...entries.map(([, v]) => v.calls)) || 1;
    entries.sort((a, b) => b[1].calls - a[1].calls);
    for (const [name, v] of entries) {
      const pct = Math.max(4, (v.calls / max) * 100);
      const row = document.createElement("div");
      row.className = "bar-row";
      row.innerHTML = `
        <div class="name">${escapeHTML(name)}</div>
        <div class="meter"><div class="fill" style="width:${pct}%"></div></div>
        <div class="num-val">${fmt(v.calls)} · ${Math.round(v.avg_latency_ms)}ms</div>
      `;
      list.appendChild(row);
    }
  }

  function renderRuntime(r) {
    const box = $("runtime");
    const items = [
      ["Go 版本", r.go_version],
      ["Goroutines", r.goroutines],
      ["堆內存 (alloc)", fmtBytes(r.heap_alloc)],
      ["堆內存 (sys)", fmtBytes(r.heap_sys)],
      ["GC 次數", r.num_gc],
      ["啟動時間", new Date(r.started_at).toLocaleString()],
      ["持續運行", r.uptime],
      ["供應商配置", r.providers],
    ];
    box.innerHTML = items
      .map(
        ([k, v]) =>
          `<div class="bar-row" style="grid-template-columns: 140px 1fr"><div class="name">${k}</div><div class="num-val" style="text-align:left">${escapeHTML(String(v ?? "—"))}</div></div>`
      )
      .join("");
  }

  function renderRecent(rows) {
    const tbody = document.querySelector("#recent-table tbody");
    if (!rows || rows.length === 0) {
      tbody.innerHTML = '<tr><td class="empty" colspan="7">尚無記錄</td></tr>';
      return;
    }
    tbody.innerHTML = rows
      .map((r) => {
        const ok = r.Status < 400;
        const cls = ok ? "ok" : "err";
        return `<tr>
          <td>${fmtTime(new Date(r.Timestamp).getTime())}</td>
          <td><span class="pill">${escapeHTML(r.Provider)}</span></td>
          <td>${escapeHTML(r.Model)}</td>
          <td class="col-status"><span class="${cls}">${r.Status}</span></td>
          <td>${r.LatencyMS} ms</td>
          <td>${fmt(r.TokensIn)} / ${fmt(r.TokensOut)}</td>
          <td>${escapeHTML(r.ClientIP || "—")}</td>
        </tr>`;
      })
      .join("");
  }

  function escapeHTML(s) {
    return String(s)
      .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;").replace(/'/g, "&#39;");
  }

  function fmtBytes(n) {
    if (!n) return "—";
    const u = ["B", "KB", "MB", "GB"]; let i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return `${n.toFixed(1)} ${u[i]}`;
  }

  async function refresh() {
    if (!tokenInput.value.trim()) {
      alert("請先輸入管理員 Token (ADMIN_TOKEN)");
      return;
    }
    refreshBtn.disabled = true;
    refreshBtn.textContent = "載入中…";
    try {
      const hrs = hoursSel.value;
      const [sum, runtime, recent] = await Promise.all([
        fetchJSON(`/api/summary?hours=${hrs}`),
        fetchJSON(`/api/runtime`),
        fetchJSON(`/api/recent?limit=50`),
      ]);
      renderSummary(sum);
      renderRuntime(runtime);
      renderRecent(recent);
    } catch (err) {
      alert("載入失敗: " + err.message);
    } finally {
      refreshBtn.disabled = false;
      refreshBtn.textContent = "刷新";
    }
  }

  refreshBtn.addEventListener("click", refresh);
  hoursSel.addEventListener("change", refresh);
  tokenInput.addEventListener("keydown", (e) => { if (e.key === "Enter") refresh(); });

  if (stored) refresh();
  setInterval(() => { if (tokenInput.value.trim()) refresh(); }, 30_000);
})();
