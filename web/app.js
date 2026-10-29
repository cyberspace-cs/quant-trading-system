/* QUANT Workbench — 前端逻辑：REST 拉取 + SSE 实时推送 + Canvas 图表 */
const API = "/api/v1";
const state = {
  view: "positions",
  cache: { positions: [], orders: [], fills: [], signals: [], risk: [], strategies: [] },
  overview: null,
};

/* ---------------- 工具 ---------------- */
const $ = (s) => document.querySelector(s);
const $$ = (s) => document.querySelectorAll(s);

function money(n) {
  const a = Math.abs(n);
  if (a >= 1e8) return (n / 1e8).toFixed(2) + " 亿";
  if (a >= 1e4) return (n / 1e4).toFixed(1) + " 万";
  return n.toFixed(0);
}
function signedMoney(n) {
  return (n >= 0 ? "+" : "-") + money(Math.abs(n));
}
function pct(n, d = 2) {
  return (n * 100).toFixed(d) + "%";
}
function num(n) {
  return Number(n).toLocaleString("en-US");
}
function cls(n) {
  return n > 0 ? "up" : n < 0 ? "down" : "";
}
function esc(s) {
  return String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
}

async function api(path, opts) {
  const res = await fetch(API + path, opts);
  if (!res.ok) throw new Error(path + " " + res.status);
  return res.json();
}

/* ---------------- 画布 ---------------- */
function prep(cv, h) {
  const dpr = window.devicePixelRatio || 1;
  const w = cv.clientWidth || 600;
  cv.style.height = h + "px";
  cv.width = Math.round(w * dpr);
  cv.height = Math.round(h * dpr);
  const ctx = cv.getContext("2d");
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  return { ctx, w, h };
}

function drawSpark(points) {
  const cv = $("#spark");
  const { ctx, w, h } = prep(cv, 120);
  ctx.clearRect(0, 0, w, h);
  if (!points || points.length < 2) return;
  const vs = points.map((p) => p.v);
  let min = Math.min(...vs), max = Math.max(...vs);
  if (max === min) { max += 0.0001; min -= 0.0001; }
  const pad = (max - min) * 0.15;
  min -= pad; max += pad;
  const X = (i) => (i / (points.length - 1)) * (w - 8) + 4;
  const Y = (v) => h - 10 - ((v - min) / (max - min)) * (h - 20);

  const grad = ctx.createLinearGradient(0, 0, 0, h);
  grad.addColorStop(0, "rgba(233,161,120,.35)");
  grad.addColorStop(1, "rgba(233,161,120,0)");
  ctx.beginPath();
  ctx.moveTo(X(0), Y(vs[0]));
  points.forEach((p, i) => ctx.lineTo(X(i), Y(p.v)));
  ctx.lineTo(X(points.length - 1), h);
  ctx.lineTo(X(0), h);
  ctx.closePath();
  ctx.fillStyle = grad;
  ctx.fill();

  ctx.beginPath();
  ctx.moveTo(X(0), Y(vs[0]));
  points.forEach((p, i) => ctx.lineTo(X(i), Y(p.v)));
  ctx.strokeStyle = "#15150f";
  ctx.lineWidth = 1.6;
  ctx.stroke();

  const last = points[points.length - 1];
  ctx.beginPath();
  ctx.arc(X(points.length - 1), Y(last.v), 3.2, 0, Math.PI * 2);
  ctx.fillStyle = "#e9a178";
  ctx.fill();
}

function drawEquity(points) {
  const cv = $("#equity");
  const { ctx, w, h } = prep(cv, 260);
  ctx.clearRect(0, 0, w, h);
  if (!points || points.length < 2) {
    ctx.fillStyle = "#b3b1a6";
    ctx.font = "13px sans-serif";
    ctx.fillText("等待行情数据…", 12, h / 2);
    return;
  }
  const vs = points.map((p) => p.v);
  let min = Math.min(...vs), max = Math.max(...vs);
  if (max === min) { max += 0.0005; min -= 0.0005; }
  const pad = (max - min) * 0.18;
  min -= pad; max += pad;
  const L = 46, R = 10, T = 14, B = 26;
  const X = (i) => L + (i / (points.length - 1)) * (w - L - R);
  const Y = (v) => T + (1 - (v - min) / (max - min)) * (h - T - B);

  // 网格
  ctx.strokeStyle = "#efece4";
  ctx.lineWidth = 1;
  ctx.fillStyle = "#b3b1a6";
  ctx.font = "10px 'DejaVu Sans Mono',monospace";
  for (let i = 0; i <= 3; i++) {
    const v = min + ((max - min) * i) / 3;
    const y = Y(v);
    ctx.beginPath();
    ctx.moveTo(L, y);
    ctx.lineTo(w - R, y);
    ctx.stroke();
    ctx.fillText(v.toFixed(4), 4, y + 3);
  }

  // 面积
  const grad = ctx.createLinearGradient(0, T, 0, h - B);
  grad.addColorStop(0, "rgba(159,184,196,.35)");
  grad.addColorStop(1, "rgba(159,184,196,0)");
  ctx.beginPath();
  ctx.moveTo(X(0), Y(vs[0]));
  points.forEach((p, i) => ctx.lineTo(X(i), Y(p.v)));
  ctx.lineTo(X(points.length - 1), h - B);
  ctx.lineTo(X(0), h - B);
  ctx.closePath();
  ctx.fillStyle = grad;
  ctx.fill();

  // 线
  ctx.beginPath();
  ctx.moveTo(X(0), Y(vs[0]));
  points.forEach((p, i) => ctx.lineTo(X(i), Y(p.v)));
  ctx.strokeStyle = "#15150f";
  ctx.lineWidth = 1.8;
  ctx.stroke();

  const last = points[points.length - 1];
  ctx.beginPath();
  ctx.arc(X(points.length - 1), Y(last.v), 3.5, 0, Math.PI * 2);
  ctx.fillStyle = "#e9a178";
  ctx.fill();

  // 时间轴
  ctx.fillStyle = "#b3b1a6";
  ctx.font = "10px 'DejaVu Sans Mono',monospace";
  const ticks = Math.min(6, points.length);
  for (let i = 0; i < ticks; i++) {
    const idx = Math.round((i / (ticks - 1)) * (points.length - 1));
    const label = points[idx].t;
    const x = X(idx);
    ctx.fillText(label, Math.min(x, w - 46), h - 8);
  }
}

/* ---------------- 渲染 ---------------- */
function statusText(s) {
  return { pre: "盘前", open: "盘中", mid: "午间", close: "盘后" }[s] || s;
}

function renderOverview(o) {
  state.overview = o;
  $("#totalAssets").textContent = money(o.total);
  const dp = o.day_pnl;
  const dpEl = $("#dayPnl");
  dpEl.textContent = signedMoney(dp);
  dpEl.className = "delta " + cls(dp);
  $("#dayPnlPct").textContent = "(" + (o.day_pnl_pct >= 0 ? "+" : "") + pct(o.day_pnl_pct) + ")";
  $("#navVal").textContent = o.nav.toFixed(4);
  $("#tradeDate").textContent = o.trade_date;
  $("#accountId").textContent = o.account_id;
  const st = statusText(o.status);
  $("#tradeStatus").textContent = st;
  $("#statusPill").textContent = st;
  $("#latency").textContent = o.latency_us;

  $("#kPnl").textContent = signedMoney(dp);
  $("#kPnl").className = "k-val " + cls(dp);
  $("#kDd").textContent = pct(o.max_drawdown);
  $("#kDd").className = "k-val " + (o.max_drawdown < 0 ? "down" : "");
  $("#kSharpe").textContent = o.sharpe.toFixed(2);
  $("#kTurn").textContent = pct(o.turnover, 1);

  drawSpark(o.equity);
  drawEquity(o.equity);
}

function renderIndustry(positions) {
  const box = $("#industry");
  if (!positions.length) { box.innerHTML = '<div class="empty">暂无持仓</div>'; return; }
  const map = {};
  positions.forEach((p) => {
    const k = p.industry || "其他";
    map[k] = (map[k] || 0) + p.market_value;
  });
  const rows = Object.entries(map).sort((a, b) => b[1] - a[1]).slice(0, 5);
  const total = rows.reduce((s, r) => s + r[1], 0) || 1;
  box.innerHTML = rows
    .map(([k, v]) => `
      <div class="ind-row">
        <span class="ind-name">${esc(k)}</span>
        <span class="ind-bar"><i style="width:${(v / total) * 100}%"></i></span>
        <span class="ind-val">${(v / total * 100).toFixed(1)}%</span>
      </div>`)
    .join("");
}

function renderStrategies(list) {
  const box = $("#strategies");
  if (!list.length) { box.innerHTML = '<div class="empty">暂无策略</div>'; return; }
  box.innerHTML = list
    .map((s) => `
      <div class="st-row">
        <div>
          <div class="st-name">${esc(s.name)}</div>
          <div class="st-sub">${esc(s.type)} · 权重 ${s.weight.toFixed(2)} · 夏普 ${s.sharpe.toFixed(2)}</div>
        </div>
        <div class="sw">
          <span class="st-pill ${s.status}">${({ on: "运行", off: "关闭", gray: "灰度", halt: "熔断" })[s.status] || s.status}</span>
          <button class="toggle ${s.status === "on" ? "on" : ""}" data-id="${esc(s.id)}" title="切换开关"></button>
        </div>
      </div>`)
    .join("");
  box.querySelectorAll(".toggle").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const id = btn.dataset.id;
      const cur = state.cache.strategies.find((x) => x.id === id);
      const next = cur && cur.status === "on" ? "off" : "on";
      try {
        await api(`/strategies/${id}/switch`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ status: next }),
        });
        await loadStrategies();
      } catch (e) { console.error(e); }
    });
  });
}

const COLS = {
  positions: [
    ["代码", (r) => `<td class="sym">${r.symbol}</td>`],
    ["数量", (r) => `<td>${num(r.qty)}</td>`],
    ["成本", (r) => `<td>${r.avg_cost.toFixed(3)}</td>`],
    ["市值", (r) => `<td>${money(r.market_value)}</td>`],
    ["盈亏", (r) => `<td class="${cls(r.pnl)}">${signedMoney(r.pnl)}</td>`],
    ["行业", (r) => `<td>${esc(r.industry || "-")}</td>`],
  ],
  orders: [
    ["时间", (r) => `<td>${r.time}</td>`],
    ["订单号", (r) => `<td>${esc(r.order_id)}</td>`],
    ["代码", (r) => `<td class="sym">${r.symbol}</td>`],
    ["方向", (r) => `<td class="${r.side === "BUY" ? "side-buy" : "side-sell"}">${r.side === "BUY" ? "买" : "卖"}</td>`],
    ["数量", (r) => `<td>${num(r.qty)}</td>`],
    ["已成交", (r) => `<td>${num(r.filled)}</td>`],
    ["价格", (r) => `<td>${r.price.toFixed(2)}</td>`],
    ["状态", (r) => `<td><span class="badge ${r.status.toLowerCase()}">${r.status}</span></td>`],
    ["策略", (r) => `<td>${esc(r.strategy)}</td>`],
  ],
  fills: [
    ["时间", (r) => `<td>${r.time}</td>`],
    ["成交号", (r) => `<td>${esc(r.fill_id)}</td>`],
    ["代码", (r) => `<td class="sym">${r.symbol}</td>`],
    ["方向", (r) => `<td class="${r.side === "BUY" ? "side-buy" : "side-sell"}">${r.side === "BUY" ? "买" : "卖"}</td>`],
    ["数量", (r) => `<td>${num(r.qty)}</td>`],
    ["价格", (r) => `<td>${r.price.toFixed(2)}</td>`],
    ["金额", (r) => `<td>${money(r.amount)}</td>`],
  ],
  signals: [
    ["时间", (r) => `<td>${r.time}</td>`],
    ["策略", (r) => `<td>${esc(r.strategy)}</td>`],
    ["代码", (r) => `<td class="sym">${r.symbol}</td>`],
    ["方向", (r) => `<td class="${r.side === "BUY" ? "side-buy" : "side-sell"}">${r.side === "BUY" ? "买" : "卖"}</td>`],
    ["数量", (r) => `<td>${num(r.qty)}</td>`],
    ["价格", (r) => `<td>${r.price.toFixed(2)}</td>`],
  ],
  risk: [
    ["时间", (r) => `<td>${r.time}</td>`],
    ["规则", (r) => `<td>${esc(r.rule_id)}</td>`],
    ["维度", (r) => `<td>${esc(r.dimension)}</td>`],
    ["动作", (r) => `<td><span class="badge rejected">${esc(r.action)}</span></td>`],
    ["原因", (r) => `<td class="serif">${esc(r.reason)}</td>`],
  ],
};

const TITLES = {
  positions: "持仓", orders: "订单", fills: "成交", signals: "信号",
  risk: "风控拦截", overview: "总览", strategies: "策略", reconcile: "对账",
};

function renderTable(view) {
  if (!COLS[view]) { return; }
  const cols = COLS[view];
  const rows = state.cache[view] || [];
  const table = $("#dataTable");
  $("#tableTitle").textContent = TITLES[view] || view;
  table.querySelector("thead").innerHTML =
    "<tr>" + cols.map((c) => `<th>${c[0]}</th>`).join("") + "</tr>";
  table.querySelector("tbody").innerHTML = rows
    .map((r) => "<tr>" + cols.map((c) => c[1](r)).join("") + "</tr>")
    .join("");
  $("#emptyHint").hidden = rows.length > 0;
  table.hidden = rows.length === 0;
}

function setView(view) {
  if (!COLS[view] && view !== "reconcile") view = "positions";
  state.view = view;
  $$(".nav-item").forEach((el) => el.classList.toggle("active", el.dataset.view === view));
  $$(".tab").forEach((el) => el.classList.toggle("active", el.dataset.view === view));
  $("#pageTitle").textContent = TITLES[view] || view;

  const panel = document.querySelector(".table-panel");
  if (view === "reconcile") {
    renderReconcile();
    return;
  }
  panel.hidden = false;
  renderTable(view);
  loadView(view);
}

async function renderReconcile() {
  const r = await api("/reconcile");
  const panel = document.querySelector(".table-panel");
  panel.hidden = false;
  $("#tableTitle").textContent = "盘后三账合一";
  const table = $("#dataTable");
  table.hidden = true;
  $("#emptyHint").hidden = true;
  panel.querySelector(".table-wrap").innerHTML = `
    <div class="recon">
      <div class="recon-card"><div class="l">对账结果</div><div class="v ${r.ok ? "recon-ok" : "recon-bad"}">${r.ok ? "一致" : r.diffs.length + " 处差异"}</div></div>
      <div class="recon-card"><div class="l">对账时间</div><div class="v">${r.time}</div></div>
      <div class="recon-card"><div class="l">差异标的</div><div class="v">${r.diffs.length}</div></div>
    </div>`;
}

/* ---------------- 数据加载 ---------------- */
async function loadOverview() {
  try { renderOverview(await api("/overview")); } catch (e) { console.error(e); }
}
async function loadStrategies() {
  try {
    state.cache.strategies = await api("/strategies");
    renderStrategies(state.cache.strategies);
  } catch (e) { console.error(e); }
}
async function loadIndustry() {
  try {
    const pos = await api("/positions");
    state.cache.positions = pos;
    renderIndustry(pos);
  } catch (e) { console.error(e); }
}
async function loadView(view) {
  const map = {
    positions: "/positions", orders: "/orders?limit=40", fills: "/fills?limit=40",
    signals: "/signals?limit=40", risk: "/risk/logs?limit=40",
  };
  if (!map[view]) return;
  try {
    state.cache[view] = await api(map[view]);
    renderTable(view);
  } catch (e) { console.error(e); }
}

async function loadAll() {
  await Promise.all([loadOverview(), loadStrategies(), loadIndustry(), loadView(state.view)]);
}

/* ---------------- 事件 ---------------- */
$$(".nav-item").forEach((el) => {
  el.addEventListener("click", (e) => { e.preventDefault(); setView(el.dataset.view); });
});
$$(".tab").forEach((el) => {
  el.addEventListener("click", () => setView(el.dataset.view));
});
$("#refreshBtn").addEventListener("click", loadAll);
window.addEventListener("resize", () => {
  if (state.overview) { drawSpark(state.overview.equity); drawEquity(state.overview.equity); }
});

/* ---------------- 实时 ---------------- */
function connectSSE() {
  try {
    const es = new EventSource(API + "/stream");
    es.addEventListener("overview", (ev) => {
      try { renderOverview(JSON.parse(ev.data)); } catch (e) {}
    });
    es.onerror = () => { es.close(); setTimeout(connectSSE, 3000); };
  } catch (e) { console.error(e); }
}

/* 启动 */
loadAll();
connectSSE();
setInterval(() => {
  loadView(state.view);
  loadIndustry();
}, 5000);
setInterval(loadStrategies, 8000);
