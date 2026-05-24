(function () {
  const $ = (id) => document.getElementById(id);
  const authPanel = $("auth-panel");
  const authTitle = $("auth-title");
  const authHelp = $("auth-help");
  const authUsername = $("auth-username");
  const authPassword = $("auth-password");
  const authSubmit = $("auth-submit");
  const authRegister = $("auth-register");
  const authMessage = $("auth-message");
  const appShell = $("app-shell");
  const signoutBtn = $("signout");
  const profileLine = $("profile-line");
  const hoursSel = $("hours");
  const refreshBtn = $("refresh");
  const providerAddBtn = $("provider-add");
  const providerReloadBtn = $("provider-reload");
  const providerSaveBtn = $("provider-save");
  const providerStatus = $("provider-status");
  const providerTableBody = document.querySelector("#providers-table tbody");
  const tokenAddBtn = $("token-add");
  const tokenReloadBtn = $("token-reload");
  const tokenSaveBtn = $("token-save");
  const tokenStatus = $("token-status");
  const tokenTableBody = document.querySelector("#tokens-table tbody");
  const viewTitle = $("view-title");
  const viewHelp = $("view-help");
  const sideLinks = Array.from(document.querySelectorAll(".side-link"));
  const viewPanels = Array.from(document.querySelectorAll(".dash-view"));
  const viewCopy = {
    overview: ["總覽指標", "查看目前窗口內的核心流量、錯誤、延遲與 Token 使用量。"],
    providers: ["供應商分佈", "比較各供應商在目前時間窗口內的請求量與平均延遲。"],
    runtime: ["運行資訊", "查看 Go runtime、記憶體、啟動時間與供應商配置數量。"],
    manage: ["供應商管理", "新增、編輯、停用或刪除 AI 供應商設定。"],
    tokens: ["Client 管理", "替每個使用者建立獨立 Token、啟用狀態、每日限制與允許模型。"],
    recent: ["最近請求", "檢視最近 API 呼叫、狀態碼、延遲與 Token 使用。"],
  };

  let providersDirty = false;
  let tokensDirty = false;
  let currentUser = null;
  let lastSummarySeries = [];
  let chartResizeTimer = null;

  signoutBtn.addEventListener("click", async (e) => {
    e.preventDefault();
    try {
      await fetchJSON("/api/auth/logout", { method: "POST", body: JSON.stringify({}) });
    } catch (_) {
      // 即使伺服器端 session 已失效，也要回到登入畫面。
    }
    currentUser = null;
    showAuth(false, "已登出，請重新登入。");
  });

  authSubmit.addEventListener("click", () => login().catch(showAuthError));
  authRegister.addEventListener("click", () => registerInitialAdmin().catch(showAuthError));
  [authUsername, authPassword].forEach((el) => {
    el.addEventListener("keydown", (e) => {
      if (e.key === "Enter") login().catch(showAuthError);
    });
  });

  async function fetchJSON(path, options = {}) {
    const headers = { ...(options.headers || {}) };
    if (options.body && !headers["Content-Type"]) headers["Content-Type"] = "application/json";
    const r = await fetch(path, { ...options, headers, credentials: "same-origin" });
    if (!r.ok) {
      const text = await r.text();
      throw new Error(`${path}: ${r.status}${text ? " - " + text.trim() : ""}`);
    }
    return r.json();
  }

  async function checkSession() {
    try {
      const profile = await fetchJSON("/api/auth/profile");
      showDashboard(profile.user);
      await refresh(true);
      return;
    } catch (_) {
      // 未登入時繼續檢查是否需要建立第一位管理員。
    }
    try {
      const boot = await fetchJSON("/api/auth/bootstrap");
      showAuth(Boolean(boot.bootstrap_required));
    } catch (err) {
      showAuth(false, "無法檢查登入狀態: " + err.message);
    }
  }

  function showAuth(bootstrapRequired, message = "") {
    authPanel.hidden = false;
    appShell.hidden = true;
    signoutBtn.hidden = true;
    currentUser = null;
    authTitle.textContent = bootstrapRequired ? "建立第一位管理員" : "登入控制台";
    authHelp.textContent = bootstrapRequired
      ? "目前尚無使用者。請建立第一位管理員帳號，之後會改用 HttpOnly Cookie 登入。"
      : "請使用管理員帳號登入。";
    authSubmit.hidden = bootstrapRequired;
    authRegister.hidden = !bootstrapRequired;
    authMessage.textContent = message || (bootstrapRequired ? "使用者名稱會自動轉為小寫並去除前後空白。" : "請輸入帳號與密碼。");
    authPassword.value = "";
    authUsername.focus();
  }

  function showDashboard(user) {
    currentUser = user;
    authPanel.hidden = true;
    appShell.hidden = false;
    signoutBtn.hidden = false;
    profileLine.textContent = `${user.username} · ${user.role}`;
    showView("overview");
  }

  function showView(view) {
    const activeView = viewCopy[view] ? view : "overview";
    sideLinks.forEach((btn) => btn.classList.toggle("active", btn.dataset.view === activeView));
    viewPanels.forEach((panel) => {
      const active = panel.dataset.viewPanel === activeView;
      panel.hidden = !active;
      panel.classList.toggle("active", active);
    });
    [viewTitle.textContent, viewHelp.textContent] = viewCopy[activeView];
    if (activeView === "overview") {
      requestAnimationFrame(() => drawKPICharts(lastSummarySeries));
    }
  }

  function showAuthError(err) {
    authMessage.textContent = err.message || String(err);
  }

  async function login() {
    authSubmit.disabled = true;
    authMessage.textContent = "登入中…";
    try {
      const res = await fetchJSON("/api/auth/login", {
        method: "POST",
        body: JSON.stringify({ username: authUsername.value, password: authPassword.value }),
      });
      showDashboard(res.user);
      await refresh(true);
    } finally {
      authSubmit.disabled = false;
    }
  }

  async function registerInitialAdmin() {
    authRegister.disabled = true;
    authMessage.textContent = "建立管理員中…";
    try {
      const res = await fetchJSON("/api/auth/register", {
        method: "POST",
        body: JSON.stringify({ username: authUsername.value, password: authPassword.value, role: "admin" }),
      });
      showDashboard(res.user);
      await refresh(true);
    } finally {
      authRegister.disabled = false;
    }
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

  function drawKPICharts(series) {
    const points = Array.isArray(series) ? series : [];
    const charts = [
      ["chart-calls", points.map((p) => Number(p.calls || 0)), "#22d3ee"],
      ["chart-errors", points.map((p) => Number(p.errors || 0)), "#fca5a5"],
      ["chart-latency", points.map((p) => Number(p.avg_latency_ms || 0)), "#a78bfa"],
      ["chart-tokens", points.map((p) => Number(p.tokens_in || 0) + Number(p.tokens_out || 0)), "#f472b6"],
    ];
    charts.forEach(([id, values, color]) => drawSparkline($(id), values, color));
  }

  function drawSparkline(canvas, values, color) {
    if (!canvas) return;
    const rect = canvas.getBoundingClientRect();
    const width = Math.max(1, Math.round(rect.width || canvas.clientWidth || 220));
    const height = Math.max(1, Math.round(rect.height || 74));
    const dpr = window.devicePixelRatio || 1;
    canvas.width = Math.round(width * dpr);
    canvas.height = Math.round(height * dpr);
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, width, height);

    const padX = 4;
    const padY = 10;
    const usableW = Math.max(1, width - padX * 2);
    const usableH = Math.max(1, height - padY * 2);
    const data = values.length ? values : [0, 0];
    const max = Math.max(...data, 0);
    const min = Math.min(...data, 0);
    const range = max - min || 1;

    ctx.strokeStyle = "rgba(148, 163, 184, 0.14)";
    ctx.lineWidth = 1;
    for (let i = 0; i < 3; i++) {
      const y = padY + (usableH / 2) * i;
      ctx.beginPath();
      ctx.moveTo(padX, y);
      ctx.lineTo(width - padX, y);
      ctx.stroke();
    }

    const coords = data.map((value, index) => {
      const x = padX + (data.length === 1 ? usableW : (usableW * index) / (data.length - 1));
      const y = padY + usableH - ((value - min) / range) * usableH;
      return [x, y];
    });

    const gradient = ctx.createLinearGradient(0, padY, 0, height - padY);
    gradient.addColorStop(0, color + "55");
    gradient.addColorStop(1, color + "00");

    ctx.beginPath();
    coords.forEach(([x, y], index) => {
      if (index === 0) ctx.moveTo(x, y);
      else ctx.lineTo(x, y);
    });
    ctx.lineTo(coords[coords.length - 1][0], height - padY);
    ctx.lineTo(coords[0][0], height - padY);
    ctx.closePath();
    ctx.fillStyle = gradient;
    ctx.fill();

    ctx.beginPath();
    coords.forEach(([x, y], index) => {
      if (index === 0) ctx.moveTo(x, y);
      else ctx.lineTo(x, y);
    });
    ctx.strokeStyle = color;
    ctx.lineWidth = 2.2;
    ctx.lineJoin = "round";
    ctx.lineCap = "round";
    ctx.shadowColor = color;
    ctx.shadowBlur = 8;
    ctx.stroke();
    ctx.shadowBlur = 0;

    const last = coords[coords.length - 1];
    ctx.beginPath();
    ctx.arc(last[0], last[1], 3.2, 0, Math.PI * 2);
    ctx.fillStyle = color;
    ctx.fill();
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
    lastSummarySeries = Array.isArray(s.series) ? s.series : [];
    drawKPICharts(lastSummarySeries);

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

  function renderProviders(providers) {
    providersDirty = false;
    if (!providers || providers.length === 0) {
      providerTableBody.innerHTML = '<tr><td class="empty" colspan="9">尚無供應商，請按「新增供應商」。</td></tr>';
      providerStatus.textContent = "目前沒有供應商。";
      return;
    }
    providerTableBody.innerHTML = providers.map(providerRow).join("");
    providerStatus.textContent = `已載入 ${providers.length} 個供應商。API Keys 只會顯示在本機瀏覽器，請勿截圖外流。`;
  }

  function providerRow(p, index) {
    return `<tr data-index="${index}">
      <td class="check-cell"><input data-field="enabled" type="checkbox" ${p.enabled ? "checked" : ""} /></td>
      <td><input data-field="name" value="${escapeAttr(p.name || "")}" placeholder="openai" /></td>
      <td><input data-field="display_name" value="${escapeAttr(p.display_name || "")}" placeholder="OpenAI" /></td>
      <td><input data-field="base_url" value="${escapeAttr(p.base_url || "")}" placeholder="https://api.openai.com" /></td>
      <td><textarea data-field="models" placeholder="gpt-4o\ngpt-4o-mini">${escapeHTML((p.models || []).join("\n"))}</textarea></td>
      <td><textarea data-field="api_keys" class="secret-text" placeholder="sk-...\nsk-...">${escapeHTML((p.api_keys || []).join("\n"))}</textarea><div class="hint">${p.key_count || (p.api_keys || []).length} 把 key</div></td>
      <td><input data-field="weight" type="number" min="1" value="${Number(p.weight || 1)}" /></td>
      <td><input data-field="timeout_sec" type="number" min="1" value="${Number(p.timeout_sec || 120)}" /></td>
      <td class="row-actions"><button class="btn danger small" data-action="delete" type="button">刪除</button></td>
    </tr>`;
  }

  function collectProviders() {
    return Array.from(providerTableBody.querySelectorAll("tr[data-index]")).map((row) => {
      const value = (field) => row.querySelector(`[data-field="${field}"]`);
      return {
        name: value("name").value.trim(),
        display_name: value("display_name").value.trim(),
        base_url: value("base_url").value.trim(),
        api_keys: lines(value("api_keys").value),
        models: lines(value("models").value),
        enabled: value("enabled").checked,
        weight: Number(value("weight").value) || 1,
        timeout_sec: Number(value("timeout_sec").value) || 120,
      };
    });
  }

  function lines(text) {
    return text.split(/\r?\n|,/).map((s) => s.trim()).filter(Boolean);
  }

  function addProvider() {
    const providers = collectProviders();
    providers.push({
      name: "",
      display_name: "",
      base_url: "",
      api_keys: [],
      models: [],
      enabled: true,
      weight: 1,
      timeout_sec: 120,
    });
    renderProviders(providers);
    markProvidersDirty();
    const lastName = providerTableBody.querySelector("tr:last-child [data-field='name']");
    if (lastName) lastName.focus();
  }

  function deleteProvider(row) {
    const name = row.querySelector('[data-field="name"]')?.value || "這個供應商";
    if (!confirm(`確定刪除「${name}」？按「儲存變更」後才會寫入設定檔。`)) return;
    row.remove();
    markProvidersDirty();
    if (!providerTableBody.querySelector("tr[data-index]")) {
      providerTableBody.innerHTML = '<tr><td class="empty" colspan="9">尚無供應商，請按「新增供應商」。</td></tr>';
    }
  }

  function markProvidersDirty() {
    providersDirty = true;
    providerStatus.textContent = "有尚未儲存的供應商變更。";
  }

  async function saveProviders() {
    const providers = collectProviders();
    providerSaveBtn.disabled = true;
    providerSaveBtn.textContent = "儲存中…";
    try {
      const res = await fetchJSON("/api/providers", {
        method: "PUT",
        body: JSON.stringify({ providers }),
      });
      providersDirty = false;
      providerStatus.textContent = `已儲存到 ${res.config_path || "目前設定檔"}。`;
      await loadProviders(true);
    } catch (err) {
      alert("儲存失敗: " + err.message);
      providerStatus.textContent = "儲存失敗，請修正欄位後再試一次。";
    } finally {
      providerSaveBtn.disabled = false;
      providerSaveBtn.textContent = "儲存變更";
    }
  }

  async function loadProviders(force = false) {
    if (providersDirty && !force) return;
    if (providersDirty && force && !confirm("目前有尚未儲存的變更，重新載入會覆蓋畫面上的修改。確定重新載入？")) return;
    const providers = await fetchJSON("/api/providers");
    renderProviders(providers);
  }

  function renderTokens(data) {
    tokensDirty = false;
    const clients = Array.isArray(data) ? data : data.clients || [];
    const envOverridden = Boolean(data && data.env_overridden);
    const legacyCount = Number((data && data.legacy_token_count) || 0);
    const warnings = [];
    if (legacyCount > 0) warnings.push(`另有 ${legacyCount} 個 legacy ACCESS_TOKENS 仍可使用。`);
    if (envOverridden) warnings.push("ACCESS_TOKENS 環境變數已設定，重新載入/重啟後可能覆蓋 legacy Token 設定。");
    if (!clients.length) {
      tokenTableBody.innerHTML = '<tr><td class="empty" colspan="10">目前沒有 Client。若也沒有 legacy ACCESS_TOKENS，/v1/* 會回 401。</td></tr>';
      tokenStatus.textContent = `目前沒有 Client；可按「新增 Client」建立給外部使用者的 Token。${warnings.length ? " " + warnings.join(" ") : ""}`;
      return;
    }
    tokenTableBody.innerHTML = clients.map(tokenRow).join("");
    tokenStatus.textContent = `已載入 ${clients.length} 個 Client。Token 預設遮蔽，請勿截圖外流。${warnings.length ? " " + warnings.join(" ") : ""}`;
  }

  function tokenRow(client, index) {
    const models = Array.isArray(client.allowed_models) ? client.allowed_models.join("\n") : "";
    const createdAt = client.created_at || "";
    const dailyLimit = Number(client.daily_limit || 0);
    const rpmLimit = Number(client.rpm_limit || 0);
    const concurrentLimit = Number(client.concurrent_limit || 0);
    return `<tr data-index="${index}">
      <td class="check-cell"><input data-field="enabled" type="checkbox" ${client.enabled ? "checked" : ""} /></td>
      <td><input data-field="name" value="${escapeAttr(client.name || "")}" placeholder="alice" autocomplete="off" /></td>
      <td><input data-field="token" class="secret-text" value="${escapeAttr(client.token || "")}" placeholder="client-token-..." autocomplete="off" spellcheck="false" /></td>
      <td><input data-field="daily_limit" type="number" min="0" value="${dailyLimit}" /><div class="hint">0 = 不限制</div></td>
      <td><input data-field="rpm_limit" type="number" min="0" value="${rpmLimit}" /><div class="hint">0 = 不限制</div></td>
      <td><input data-field="concurrent_limit" type="number" min="0" value="${concurrentLimit}" /><div class="hint">0 = 不限制</div></td>
      <td><textarea data-field="allowed_models" placeholder="gpt-4o-mini\ndeepseek-chat">${escapeHTML(models)}</textarea><div class="hint">空白 = 全部模型</div></td>
      <td><textarea data-field="note" placeholder="用途、聯絡方式或備註">${escapeHTML(client.note || "")}</textarea></td>
      <td><span class="muted-cell">${createdAt ? escapeHTML(new Date(createdAt).toLocaleString()) : "儲存時建立"}</span><input data-field="created_at" type="hidden" value="${escapeAttr(createdAt)}" /></td>
      <td class="row-actions"><button class="btn danger small" data-action="delete-token" type="button">刪除</button></td>
    </tr>`;
  }

  function collectTokens() {
    return Array.from(tokenTableBody.querySelectorAll('tr[data-index]')).map((row) => {
      const value = (field) => row.querySelector(`[data-field="${field}"]`);
      return {
        enabled: value("enabled").checked,
        name: value("name").value.trim(),
        token: value("token").value.trim(),
        daily_limit: Math.max(0, Number(value("daily_limit").value) || 0),
        rpm_limit: Math.max(0, Number(value("rpm_limit").value) || 0),
        concurrent_limit: Math.max(0, Number(value("concurrent_limit").value) || 0),
        allowed_models: lines(value("allowed_models").value),
        note: value("note").value.trim(),
        created_at: value("created_at").value.trim(),
      };
    });
  }

  function addToken() {
    const clients = collectTokens();
    clients.push({ enabled: true, name: "", token: "", daily_limit: 0, rpm_limit: 0, concurrent_limit: 0, allowed_models: [], note: "", created_at: "" });
    tokenTableBody.innerHTML = clients.map(tokenRow).join("");
    markTokensDirty();
    const lastName = tokenTableBody.querySelector("tr:last-child [data-field='name']");
    if (lastName) lastName.focus();
  }

  function deleteToken(row) {
    const name = row.querySelector('[data-field="name"]')?.value || "這個 Client";
    if (!confirm(`確定刪除「${name}」？按「儲存變更」後才會寫入設定檔。`)) return;
    row.remove();
    markTokensDirty();
    if (!tokenTableBody.querySelector("tr[data-index]")) {
      tokenTableBody.innerHTML = '<tr><td class="empty" colspan="10">目前沒有 Client。儲存空清單後，若也沒有 legacy ACCESS_TOKENS，/v1/* 會拒絕請求。</td></tr>';
    }
  }

  function markTokensDirty() {
    tokensDirty = true;
    tokenStatus.textContent = "有尚未儲存的 Client 變更。";
  }

  async function saveTokens() {
    const clients = collectTokens();
    if (!clients.length && !confirm("儲存空 Client 清單後，若沒有 legacy ACCESS_TOKENS，/v1/* 會全部 401。確定嗎？")) return;
    tokenSaveBtn.disabled = true;
    tokenSaveBtn.textContent = "儲存中…";
    try {
      const res = await fetchJSON("/api/clients", {
        method: "PUT",
        body: JSON.stringify({ clients }),
      });
      tokensDirty = false;
      tokenStatus.textContent = `已儲存 ${res.count ?? clients.length} 個 Client 到 ${res.config_path || "目前設定檔"}。${res.legacy_token_count ? `另有 ${res.legacy_token_count} 個 legacy ACCESS_TOKENS 仍可使用。` : ""}${res.env_overridden ? " 注意：ACCESS_TOKENS 環境變數已設定，重新載入/重啟後可能覆蓋 legacy Token 設定。" : ""}`;
      await loadTokens(true);
    } catch (err) {
      alert("儲存 Client 失敗: " + err.message);
      tokenStatus.textContent = "儲存失敗，請檢查名稱/Token 是否空白、重複或包含空白字元。";
    } finally {
      tokenSaveBtn.disabled = false;
      tokenSaveBtn.textContent = "儲存變更";
    }
  }

  async function loadTokens(force = false) {
    if (tokensDirty && !force) return;
    if (tokensDirty && force && !confirm("目前有尚未儲存的 Client 變更，重新載入會覆蓋畫面上的修改。確定重新載入？")) return;
    const data = await fetchJSON("/api/clients");
    renderTokens(data);
  }

  function escapeHTML(s) {
    return String(s)
      .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;").replace(/'/g, "&#39;");
  }

  function escapeAttr(s) {
    return escapeHTML(s).replace(/`/g, "&#96;");
  }

  function fmtBytes(n) {
    if (!n) return "—";
    const u = ["B", "KB", "MB", "GB"]; let i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return `${n.toFixed(1)} ${u[i]}`;
  }

  async function refresh(loadProviderConfig = true) {
    if (!currentUser) return;
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
      if (loadProviderConfig) {
        await loadProviders(false);
        await loadTokens(false);
      }
    } catch (err) {
      alert("載入失敗: " + err.message);
    } finally {
      refreshBtn.disabled = false;
      refreshBtn.textContent = "刷新";
    }
  }

  providerTableBody.addEventListener("input", (e) => {
    if (e.target.matches("input, textarea")) markProvidersDirty();
  });
  providerTableBody.addEventListener("change", (e) => {
    if (e.target.matches("input, textarea")) markProvidersDirty();
  });
  providerTableBody.addEventListener("click", (e) => {
    const btn = e.target.closest('[data-action="delete"]');
    if (btn) deleteProvider(btn.closest("tr"));
  });
  tokenTableBody.addEventListener("input", (e) => {
    if (e.target.matches("input, textarea")) markTokensDirty();
  });
  tokenTableBody.addEventListener("change", (e) => {
    if (e.target.matches("input, textarea")) markTokensDirty();
  });
  tokenTableBody.addEventListener("click", (e) => {
    const btn = e.target.closest('[data-action="delete-token"]');
    if (btn) deleteToken(btn.closest("tr"));
  });

  sideLinks.forEach((btn) => btn.addEventListener("click", () => showView(btn.dataset.view)));
  providerAddBtn.addEventListener("click", addProvider);
  providerSaveBtn.addEventListener("click", saveProviders);
  providerReloadBtn.addEventListener("click", () => loadProviders(true).catch((err) => alert("載入供應商失敗: " + err.message)));
  tokenAddBtn.addEventListener("click", addToken);
  tokenSaveBtn.addEventListener("click", saveTokens);
  tokenReloadBtn.addEventListener("click", () => loadTokens(true).catch((err) => alert("載入 Clients 失敗: " + err.message)));
  refreshBtn.addEventListener("click", () => refresh(true));
  hoursSel.addEventListener("change", () => refresh(false));
  window.addEventListener("resize", () => {
    clearTimeout(chartResizeTimer);
    chartResizeTimer = setTimeout(() => drawKPICharts(lastSummarySeries), 120);
  });

  // Handle page transitions
  document.body.style.opacity = "1";
  document.querySelectorAll("a").forEach(link => {
    link.addEventListener("click", (e) => {
      const href = link.getAttribute("href");
      if (href && href.startsWith("/") && !href.startsWith("//") && !e.ctrlKey && !e.metaKey && !e.shiftKey) {
        e.preventDefault();
        document.body.classList.add("fade-out");
        setTimeout(() => { location.href = href; }, 300);
      }
    });
  });

  checkSession();
  setInterval(() => { if (currentUser) refresh(false); }, 30_000);
})();
