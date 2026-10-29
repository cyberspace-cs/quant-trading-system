/* ============================================================================
   QUANT Workbench v2 —— 交易工作台前端
   · 零依赖原生 JS：登录门禁 / 14 个视图 / WebSocket 实时 / Canvas 图表
   · 所有写操作走 REST，按钮按 RBAC 权限隐藏（服务端仍然强校验）
   ========================================================================== */
const API = "/api/v1";

const S = {
  token: localStorage.getItem("q.token") || "",
  refreshToken: localStorage.getItem("q.refresh") || "",
  user: null,
  perms: [],
  view: "dashboard",
  ov: null,
  page: {},
  cache: {},
  ws: null,
  wsAlive: false,
  timer: null,
  lastFocus: false,
};

/* ---------------- 基础工具 ---------------- */
const $ = (s) => document.querySelector(s);
const $$ = (s) => Array.from(document.querySelectorAll(s));
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const num = (n) => Number(n ?? 0).toLocaleString("en-US");
const money = (n) => {
  const a = Math.abs(n || 0);
  if (a >= 1e8) return (n / 1e8).toFixed(2) + " 亿";
  if (a >= 1e4) return (n / 1e4).toFixed(1) + " 万";
  return (n || 0).toFixed(0);
};
const signedMoney = (n) => (n >= 0 ? "+" : "-") + money(Math.abs(n));
const pct = (n, d = 2) => ((n || 0) * 100).toFixed(d) + "%";
const signedPct = (n, d = 2) => (n >= 0 ? "+" : "") + pct(n, d);
const cls = (n) => (n > 0 ? "up" : n < 0 ? "down" : "");
const can = (p) => S.perms.includes(p) || S.perms.includes("admin");

function toast(msg, bad) {
  const el = $("#toast");
  el.textContent = msg;
  el.className = "toast" + (bad ? " bad" : "");
  el.hidden = false;
  clearTimeout(el._t);
  el._t = setTimeout(() => (el.hidden = true), bad ? 4200 : 2400);
}

/* ---------------- HTTP ---------------- */
async function api(path, opt = {}) {
  const { method = "GET", body, headers = {}, meta = false, raw = false } = opt;
  const h = { ...headers };
  if (body !== undefined) h["Content-Type"] = "application/json";
  if (S.token) h.Authorization = "Bearer " + S.token;
  const res = await fetch(API + path, {
    method,
    headers: h,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (res.status === 401) {
    logout(true);
    throw new Error("登录已过期，请重新登录");
  }
  if (raw) return res;
  const j = await res.json().catch(() => ({ code: -1, msg: "响应解析失败" }));
  if (j.code !== 0) throw new Error(j.msg || "请求失败 HTTP " + res.status);
  return meta ? { data: j.data, meta: j.meta } : j.data !== undefined ? j.data : j;
}

/* ---------------- Canvas ---------------- */
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

function drawLine(cv, values, opt = {}) {
  if (!cv) return;
  const { height = 240, color = "#15150f", fill = "rgba(159,184,196,.28)", pad = true, labels = null } = opt;
  const { ctx, w, h } = prep(cv, height);
  ctx.clearRect(0, 0, w, h);
  if (!values || values.length < 2) {
    ctx.fillStyle = "#b3b1a6";
    ctx.font = "12px sans-serif";
    ctx.fillText("等待数据…", 12, h / 2);
    return;
  }
  let min = Math.min(...values), max = Math.max(...values);
  if (min === max) { max += 0.001; min -= 0.001; }
  const p = (max - min) * 0.16;
  min -= p; max += p;
  const L = pad ? 44 : 8, R = 8, T = 12, B = pad ? 22 : 8;
  const X = (i) => L + (i / (values.length - 1)) * (w - L - R);
  const Y = (v) => T + (1 - (v - min) / (max - min)) * (h - T - B);

  if (pad) {
    ctx.strokeStyle = "#efece4";
    ctx.fillStyle = "#b3b1a6";
    ctx.font = "9.5px 'DejaVu Sans Mono',monospace";
    for (let i = 0; i <= 3; i++) {
      const v = min + ((max - min) * i) / 3;
      const y = Y(v);
      ctx.beginPath(); ctx.moveTo(L, y); ctx.lineTo(w - R, y); ctx.stroke();
      ctx.fillText(v.toFixed(4), 4, y + 3);
    }
  }
  const grad = ctx.createLinearGradient(0, T, 0, h - B);
  grad.addColorStop(0, fill);
  grad.addColorStop(1, "rgba(255,255,255,0)");
  ctx.beginPath();
  ctx.moveTo(X(0), Y(values[0]));
  values.forEach((v, i) => ctx.lineTo(X(i), Y(v)));
  ctx.lineTo(X(values.length - 1), h - B);
  ctx.lineTo(X(0), h - B);
  ctx.closePath();
  ctx.fillStyle = grad;
  ctx.fill();

  ctx.beginPath();
  ctx.moveTo(X(0), Y(values[0]));
  values.forEach((v, i) => ctx.lineTo(X(i), Y(v)));
  ctx.strokeStyle = color;
  ctx.lineWidth = 1.6;
  ctx.stroke();

  ctx.beginPath();
  ctx.arc(X(values.length - 1), Y(values[values.length - 1]), 3, 0, Math.PI * 2);
  ctx.fillStyle = "#e9a178";
  ctx.fill();

  if (labels && labels.length === values.length && pad) {
    ctx.fillStyle = "#b3b1a6";
    ctx.font = "9.5px 'DejaVu Sans Mono',monospace";
    const n = Math.min(5, values.length);
    for (let i = 0; i < n; i++) {
      const idx = Math.round((i / (n - 1)) * (values.length - 1));
      const x = X(idx);
      ctx.fillText(labels[idx], Math.min(x, w - 44), h - 6);
    }
  }
}

/* ---------------- 登录 / 会话 ---------------- */
async function doLogin(u, p) {
  const res = await fetch(API + "/auth/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username: u, password: p }),
  });
  const j = await res.json();
  if (j.code !== 0) throw new Error(j.msg || "登录失败");
  S.token = j.data.access_token;
  S.refreshToken = j.data.refresh_token;
  S.user = j.data.user;
  S.perms = j.data.perms || [];
  localStorage.setItem("q.token", S.token);
  localStorage.setItem("q.refresh", S.refreshToken);
  localStorage.setItem("q.perms", JSON.stringify(S.perms));
  localStorage.setItem("q.user", JSON.stringify(S.user));
}

function logout(expired) {
  if (S.token && !expired) api("/auth/logout", { method: "POST" }).catch(() => {});
  if (S.ws) { try { S.ws.close(); } catch (e) {} S.ws = null; }
  clearTimeout(S.timer);
  S.token = ""; S.user = null; S.perms = [];
  localStorage.removeItem("q.token");
  localStorage.removeItem("q.refresh");
  localStorage.removeItem("q.perms");
  localStorage.removeItem("q.user");
  $("#app").hidden = true;
  $("#login").hidden = false;
  if (expired) { $("#loginErr").hidden = false; $("#loginErr").textContent = "登录已过期，请重新登录"; }
}

async function restore() {
  const t = S.token;
  if (!t) return false;
  try {
    const me = await api("/auth/me");
    S.user = me.user; S.perms = me.perms;
    return true;
  } catch (e) {
    return false;
  }
}

/* ============================================================================
   视图定义
   ========================================================================== */
const TITLES = {
  dashboard: "总览", positions: "持仓", orders: "订单", fills: "成交", signals: "信号",
  risk: "风控", strategies: "策略", factors: "因子", backtests: "回测",
  reports: "报表", capital: "资金", audit: "审计", system: "系统", reconcile: "对账",
};

/* ---------------- 01 总览 ---------------- */
async function vDashboard() {
  const ov = await api("/overview");
  const pos = await api("/positions");
  const strat = await api("/strategies");
  const limits = await api("/risk/limits");
  const orders = (await api("/orders?size=6", { meta: true })).data || [];
  S.ov = ov;
  const eq = (ov.equity || []).map((x) => x.v);
  const st = ov.status;

  return `
  <section class="hero">
    <div class="hero-blobs"><span class="blob b1"></span><span class="blob b2"></span><span class="blob b3"></span></div>
    <div class="hero-left">
      <div class="label">账户总资产 · 实时盯市</div>
      <div class="big">${money(ov.total)}</div>
      <div class="sub">
        <span class="delta ${cls(ov.day_pnl)}">${signedMoney(ov.day_pnl)}</span>
        <span class="${cls(ov.day_pnl)}">(${signedPct(ov.day_pnl_pct)})</span>
        <span class="sep"></span><span>净值</span><b>${ov.nav.toFixed(4)}</b>
        <span class="sep"></span><span>${ov.trade_date}</span>
      </div>
    </div>
    <div class="hero-right"><canvas id="spark"></canvas></div>
  </section>

  <section class="kpis">
    <div class="kpi"><div class="k-label">当日盈亏</div><div class="k-val ${cls(ov.day_pnl)}">${signedMoney(ov.day_pnl)}</div><div class="k-hint">已实现 + 浮动</div></div>
    <div class="kpi"><div class="k-label">最大回撤</div><div class="k-val ${ov.max_drawdown < 0 ? "down" : ""}">${pct(ov.max_drawdown)}</div><div class="k-hint">自峰值回撤</div></div>
    <div class="kpi"><div class="k-label">夏普比率</div><div class="k-val">${ov.sharpe.toFixed(2)}</div><div class="k-hint">年化滚动</div></div>
    <div class="kpi"><div class="k-label">换手率</div><div class="k-val">${pct(ov.turnover, 1)}</div><div class="k-hint">成交额 / 资产 / 2</div></div>
  </section>

  <section class="grid-2">
    <div class="panel">
      <div class="panel-head"><h2>净值曲线</h2><span class="tag">${eq.length} 点 · WS 实时</span></div>
      <canvas id="equity"></canvas>
    </div>
    <div class="panel">
      <div class="panel-head"><h2>额度占用</h2><span class="tag">事前风控</span></div>
      <div>${(limits.limits || []).slice(0, 5).map(limitRow).join("") || '<div class="empty">暂无规则</div>'}</div>
    </div>
  </section>

  <section class="grid-2">
    <div class="panel">
      <div class="panel-head"><h2>行业暴露</h2><span class="tag">占持仓市值</span></div>
      <div>${industryBars(pos)}</div>
    </div>
    <div class="panel">
      <div class="panel-head"><h2>策略运行态</h2><span class="tag">可灰度 / 熔断</span></div>
      <div>${strat.map(strategyRow).join("")}</div>
    </div>
  </section>

  <section class="panel">
    <div class="panel-head"><h2>最新委托</h2><span class="tag">${ov.orders} 单 / ${ov.fills} 笔成交 / ${ov.risk_rejects} 次拦截</span></div>
    ${tableHTML(["时间", "订单号", "代码", "方向", "数量", "已成交", "价格", "状态"], orders.map((r) => [
      r.time, `<span class="sym">${esc(r.order_id)}</span>`, `<span class="sym">${r.symbol}</span>`,
      `<span class="${r.side === "BUY" ? "side-buy" : "side-sell"}">${r.side === "BUY" ? "买" : "卖"}</span>`,
      num(r.qty), num(r.filled), r.price.toFixed(2), `<span class="badge ${r.status.toLowerCase()}">${r.status}</span>`,
    ]))}
  </section>`;
}

function mountDashboard() {
  const ov = S.ov;
  if (!ov) return;
  const eq = (ov.equity || []).map((x) => x.v);
  drawLine($("#spark"), eq, { height: 110, pad: false, color: "#15150f", fill: "rgba(233,161,120,.32)" });
  drawLine($("#equity"), eq, { height: 250, labels: (ov.equity || []).map((x) => x.t) });
  $$("#view .toggle").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const id = btn.dataset.id;
      const next = btn.classList.contains("on") ? "off" : "on";
      try {
        await api(`/strategies/${id}/switch`, { method: "POST", body: { status: next, reason: "工作台操作" } });
        toast(`策略 ${id} -> ${next}`);
        render();
      } catch (e) { toast(e.message, true); }
    });
  });
}

function industryBars(pos) {
  if (!pos.length) return '<div class="empty">暂无持仓</div>';
  const m = {};
  pos.forEach((p) => { const k = p.industry || "其他"; m[k] = (m[k] || 0) + p.market_value; });
  const rows = Object.entries(m).sort((a, b) => b[1] - a[1]).slice(0, 6);
  const total = rows.reduce((s, r) => s + r[1], 0) || 1;
  return rows.map(([k, v]) => `
    <div class="ind-row">
      <span>${esc(k)}</span>
      <span class="ind-bar"><i style="width:${(v / total) * 100}%"></i></span>
      <span class="ind-val">${((v / total) * 100).toFixed(1)}%</span>
    </div>`).join("");
}

function strategyRow(s) {
  const label = { on: "运行", off: "关闭", gray: "灰度", halt: "熔断" }[s.status] || s.status;
  return `<div class="st-row">
    <div>
      <div class="st-name">${esc(s.name)}</div>
      <div class="st-sub">${esc(s.type)} · 权重 ${Number(s.weight).toFixed(2)} · 夏普 ${Number(s.sharpe).toFixed(2)} · v${s.param_version ?? 1}</div>
    </div>
    <div class="sw">
      <span class="badge ${s.status}">${label}</span>
      <button class="toggle ${s.status === "on" ? "on" : ""}" data-id="${esc(s.id)}" title="切换"></button>
    </div>
  </div>`;
}

function limitRow(l) {
  const w = Math.min(100, Math.abs(l.usage_pct || 0) * 100);
  return `<div class="limit">
    <div><div class="limit-name">${esc(l.name)}</div>
      <div class="limit-sub">阈值 ${fmtTh(l)} · 当前 ${fmtTh({ ...l, threshold: l.current })}</div></div>
    <div class="limit-val">${(Math.abs(l.usage_pct || 0) * 100).toFixed(0)}%</div>
    <div class="limit-bar"><i class="${l.status}" style="width:${w}%"></i></div>
  </div>`;
}
function fmtTh(l) {
  const d = l.dimension;
  if (d === "lot" || d === "turnover") return num(Math.round(l.threshold));
  if (d === "pnl" || d === "drawdown" || d === "position" || d === "industry") return pct(l.threshold, 1);
  return String(l.threshold);
}

function tableHTML(headers, rows, empty = "暂无数据") {
  if (!rows.length) return `<div class="empty">${empty}</div>`;
  return `<div class="table-wrap"><table>
    <thead><tr>${headers.map((h) => `<th>${h}</th>`).join("")}</tr></thead>
    <tbody>${rows.map((r) => `<tr>${r.map((c) => `<td>${c}</td>`).join("")}</tr>`).join("")}</tbody>
  </table></div>`;
}

/* ---------------- 02 持仓 ---------------- */
async function vPositions() {
  const pos = await api("/positions");
  const total = pos.reduce((s, p) => s + p.market_value, 0);
  return `
  <div class="cards">
    <div class="card"><div class="l">持仓市值</div><div class="v">${money(total)}</div><div class="h">${pos.length} 个标的</div></div>
    <div class="card"><div class="l">浮动盈亏</div><div class="v ${cls(pos.reduce((s, p) => s + p.pnl, 0))}">${signedMoney(pos.reduce((s, p) => s + p.pnl, 0))}</div><div class="h">按最新价盯市</div></div>
    <div class="card"><div class="l">最大单票</div><div class="v">${pos.length ? pct((pos[0].market_value || 0) / (total || 1), 1) : "—"}</div><div class="h">${pos.length ? pos[0].symbol : "—"}</div></div>
    <div class="card"><div class="l">行业数</div><div class="v">${new Set(pos.map((p) => p.industry)).size}</div><div class="h">集中度监控中</div></div>
  </div>
  <div class="panel" style="margin-top:18px">
    <div class="panel-head"><h2>持仓明细</h2><span class="tag">涨红跌绿</span></div>
    ${tableHTML(["代码", "数量", "成本", "市值", "盈亏", "盈亏率", "行业"], pos.map((p) => [
      `<span class="sym">${p.symbol}</span>`, num(p.qty), p.avg_cost.toFixed(3), money(p.market_value),
      `<span class="${cls(p.pnl)}">${signedMoney(p.pnl)}</span>`,
      `<span class="${cls(p.pnl)}">${signedPct(p.pnl_pct)}</span>`, esc(p.industry || "-"),
    ]))}
  </div>`;
}

/* ---------------- 03 订单 ---------------- */
async function vOrders() {
  const page = S.page.orders || 1;
  const q = S.cache.ordersQ || "";
  const status = S.cache.ordersStatus || "";
  const { data, meta } = await api(`/orders?page=${page}&size=12&q=${encodeURIComponent(q)}&status=${status}`, { meta: true });
  const orders = data || [];
  const editable = can("order:write");

  return `
  <div class="panel">
    <div class="panel-head"><h2>手动下单</h2><span class="tag">事前风控 → 智能拆单 → 柜台</span></div>
    ${editable ? `
    <form id="orderForm" class="form-grid">
      <label class="field"><span>代码</span><input name="symbol" value="000001" list="symList" required></label>
      <datalist id="symList">${(S.cache.symbols || []).map((s) => `<option value="${s}">`).join("")}</datalist>
      <label class="field"><span>方向</span><select name="side"><option value="BUY">买入</option><option value="SELL">卖出</option></select></label>
      <label class="field"><span>数量（股）</span><input name="qty" type="number" value="1000" step="100" min="100" required></label>
      <label class="field"><span>价格（0=最新价）</span><input name="price" type="number" value="0" step="0.01" min="0"></label>
      <label class="field"><span>拆单算法</span><select name="algo"><option>TWAP</option><option>VWAP</option><option>ADAPTIVE</option></select></label>
      <label class="field"><span>切片数</span><input name="slices" type="number" value="3" min="1" max="20"></label>
      <label class="field"><span>策略归属</span><select name="strategy_id">
        <option value="manual">manual</option>
        ${(S.cache.strategies || []).map((s) => `<option value="${s.id}">${s.id}</option>`).join("")}
      </select></label>
      <div class="actions"><button class="btn primary" type="submit">下 单</button></div>
      <div class="form-note">下单前会经过事前风控（仓位/行业/手数/涨跌停/黑名单），违规直接拦截不进柜台；写操作留审计痕迹。</div>
    </form>` : `<div class="empty">当前角色无下单权限（需要 order:write）</div>`}
  </div>

  <div class="panel" style="margin-top:18px">
    <div class="panel-head">
      <h2>委托流水</h2>
      <div class="toolbar" style="margin:0">
        <input type="search" id="oq" placeholder="搜索订单号 / 代码" value="${esc(q)}">
        <select id="ostatus">
          <option value="">全部状态</option>
          ${["SENT", "PARTIAL", "FILLED", "CANCELLED", "REJECTED"].map((s) => `<option value="${s}" ${s === status ? "selected" : ""}>${s}</option>`).join("")}
        </select>
        ${editable ? `<button class="btn tiny" data-act="batchCancel">批量撤单</button>` : ""}
        <span class="spacer"></span>
        <span class="tag">${meta ? meta.total : 0} 条 · 第 ${meta ? meta.page : 1}/${meta ? meta.pages : 1} 页</span>
        <button class="btn tiny" data-act="page" data-dir="-1" ${page <= 1 ? "disabled" : ""}>上一页</button>
        <button class="btn tiny" data-act="page" data-dir="1" ${meta && page >= meta.pages ? "disabled" : ""}>下一页</button>
      </div>
    </div>
    ${tableHTML(["时间", "订单号", "代码", "方向", "数量", "已成交", "价格", "状态", "策略", "操作"], orders.map((o) => [
      o.time, `<span class="sym">${esc(o.order_id)}</span>`, `<span class="sym">${o.symbol}</span>`,
      `<span class="${o.side === "BUY" ? "side-buy" : "side-sell"}">${o.side === "BUY" ? "买" : "卖"}</span>`,
      num(o.qty), num(o.filled), o.price.toFixed(2),
      `<span class="badge ${o.status.toLowerCase()}">${o.status}</span>`, esc(o.strategy),
      `<button class="btn tiny" data-act="orderDetail" data-id="${esc(o.order_id)}">详情</button>
       ${editable && (o.status === "SENT" || o.status === "PARTIAL") ? `<button class="btn tiny danger" data-act="cancelOrder" data-id="${esc(o.order_id)}">撤单</button>` : ""}`,
    ]))}
  </div>`;
}

function mountOrders() {
  const form = $("#orderForm");
  if (form) {
    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      const fd = new FormData(form);
      const body = {
        symbol: fd.get("symbol").trim(),
        side: fd.get("side"),
        qty: Number(fd.get("qty")),
        price: Number(fd.get("price")),
        algo: fd.get("algo"),
        slices: Number(fd.get("slices")),
        strategy_id: fd.get("strategy_id"),
      };
      const btn = form.querySelector("button");
      btn.disabled = true;
      try {
        const r = await api("/orders", {
          method: "POST", body,
          headers: { "Idempotency-Key": "ui-" + Date.now() + "-" + Math.random().toString(16).slice(2, 8) },
        });
        toast(`下单成功 ${r.parent.order_id} · ${r.children.length} 张子单 · ${r.fill_count} 笔成交 · ${r.latency_us}µs`);
        render();
      } catch (err) {
        toast(err.message, true);
      } finally {
        btn.disabled = false;
      }
    });
  }
  const oq = $("#oq"), os = $("#ostatus");
  if (oq) {
    let t;
    oq.addEventListener("input", () => {
      clearTimeout(t);
      t = setTimeout(() => { S.cache.ordersQ = oq.value; S.page.orders = 1; render(); }, 320);
    });
  }
  if (os) os.addEventListener("change", () => { S.cache.ordersStatus = os.value; S.page.orders = 1; render(); });
}

async function orderDetail(id) {
  const d = await api(`/orders/${encodeURIComponent(id)}`);
  const o = d.order;
  openModal(`订单 ${o.order_id}`, `
    <dl class="kv">
      <dt>标的</dt><dd>${o.symbol}</dd>
      <dt>方向 / 数量</dt><dd>${o.side} ${num(o.qty)} 股（已成交 ${num(o.filled)}）</dd>
      <dt>价格</dt><dd>${o.price.toFixed(2)}</dd>
      <dt>状态</dt><dd><span class="badge ${o.status.toLowerCase()}">${o.status}</span></dd>
      <dt>策略</dt><dd>${esc(o.strategy)}</dd>
      <dt>可改单</dt><dd>${d.amendable ? "是" : "否"}</dd>
    </dl>
    <h4 style="font:600 13px var(--serif);margin:16px 0 8px">子单（拆单）</h4>
    ${tableHTML(["子单号", "数量", "已成交", "价格", "状态"], (d.children || []).map((c) => [
      `<span class="sym">${esc(c.order_id)}</span>`, num(c.qty), num(c.filled), c.price.toFixed(2),
      `<span class="badge ${c.status.toLowerCase()}">${c.status}</span>`,
    ]), "无子单（未拆单）")}
    <h4 style="font:600 13px var(--serif);margin:16px 0 8px">成交明细</h4>
    ${tableHTML(["成交号", "数量", "价格", "金额", "时间"], (d.fills || []).map((f) => [
      `<span class="sym">${esc(f.fill_id)}</span>`, num(f.qty), f.price.toFixed(2), money(f.amount), f.time,
    ]), "暂无成交")}
    ${d.amendable && can("order:write") ? `
      <h4 style="font:600 13px var(--serif);margin:16px 0 8px">改单</h4>
      <form class="form-grid" id="amendForm">
        <label class="field"><span>新数量</span><input name="qty" type="number" value="${o.qty}" step="100"></label>
        <label class="field"><span>新价格</span><input name="price" type="number" value="${o.price}" step="0.01"></label>
        <div class="actions"><button class="btn" type="submit">提交改单</button></div>
      </form>` : ""}
  `);
  const af = $("#amendForm");
  if (af) {
    af.addEventListener("submit", async (e) => {
      e.preventDefault();
      const fd = new FormData(af);
      try {
        await api(`/orders/${encodeURIComponent(o.order_id)}`, {
          method: "PUT",
          body: { qty: Number(fd.get("qty")), price: Number(fd.get("price")), reason: "工作台改单" },
        });
        toast("改单成功");
        closeModal();
        render();
      } catch (err) { toast(err.message, true); }
    });
  }
}

/* ---------------- 04 成交 / 05 信号 ---------------- */
async function vFills() {
  const { data, meta } = await api("/fills?size=80", { meta: true });
  const rows = data || [];
  const amt = rows.reduce((s, f) => s + f.amount, 0);
  return `
  <div class="cards">
    <div class="card"><div class="l">成交笔数</div><div class="v">${meta ? meta.total : rows.length}</div><div class="h">全量（含历史）</div></div>
    <div class="card"><div class="l">展示区间成交额</div><div class="v">${money(amt)}</div><div class="h">最近 ${rows.length} 笔</div></div>
    <div class="card"><div class="l">主动买 / 卖</div><div class="v">${rows.filter((r) => r.side === "BUY").length} / ${rows.filter((r) => r.side === "SELL").length}</div><div class="h">按笔数</div></div>
  </div>
  <div class="panel" style="margin-top:18px">
    <div class="panel-head"><h2>成交明细</h2><span class="tag">柜台回执为唯一真值</span></div>
    ${tableHTML(["时间", "成交号", "代码", "方向", "数量", "价格", "金额"], rows.map((f) => [
      f.time, `<span class="sym">${esc(f.fill_id)}</span>`, `<span class="sym">${f.symbol}</span>`,
      `<span class="${f.side === "BUY" ? "side-buy" : "side-sell"}">${f.side === "BUY" ? "买" : "卖"}</span>`,
      num(f.qty), f.price.toFixed(2), money(f.amount),
    ]))}
  </div>`;
}

async function vSignals() {
  const { data, meta } = await api("/signals?size=80", { meta: true });
  const rows = data || [];
  const m = {};
  rows.forEach((r) => (m[r.strategy] = (m[r.strategy] || 0) + 1));
  return `
  <div class="cards">
    <div class="card"><div class="l">信号总数</div><div class="v">${meta ? meta.total : rows.length}</div><div class="h">策略输出 → 风控 → 执行</div></div>
    ${Object.entries(m).slice(0, 3).map(([k, v]) => `<div class="card"><div class="l">${esc(k)}</div><div class="v">${v}</div><div class="h">条信号</div></div>`).join("")}
  </div>
  <div class="panel" style="margin-top:18px">
    <div class="panel-head"><h2>信号流水</h2><span class="tag">多因子打分排序</span></div>
    ${tableHTML(["时间", "策略", "代码", "方向", "数量", "参考价"], rows.map((s) => [
      s.time, esc(s.strategy), `<span class="sym">${s.symbol}</span>`,
      `<span class="${s.side === "BUY" ? "side-buy" : "side-sell"}">${s.side === "BUY" ? "买" : "卖"}</span>`,
      num(s.qty), s.price.toFixed(2),
    ]))}
  </div>`;
}

/* ---------------- 06 风控 ---------------- */
async function vRisk() {
  const limits = await api("/risk/limits");
  const rules = (await api("/risk/rules?size=100", { meta: true })).data || [];
  const logs = (await api("/risk/logs?size=20", { meta: true })).data || [];
  const rw = can("risk:write");
  const ap = can("approve");

  return `
  <div class="grid-2">
    <div class="panel">
      <div class="panel-head"><h2>额度占用</h2><span class="tag">总资产 ${money(limits.total_assets)}</span></div>
      ${(limits.limits || []).map(limitRow).join("") || '<div class="empty">暂无规则</div>'}
    </div>
    <div class="panel">
      <div class="panel-head"><h2>行业暴露</h2><span class="tag">事前拦截口径</span></div>
      ${(limits.industry_exposure || []).length ? limits.industry_exposure.map((x) => `
        <div class="ind-row"><span>${esc(x.industry)}</span>
          <span class="ind-bar"><i style="width:${Math.min(100, x.ratio * 300)}%"></i></span>
          <span class="ind-val">${pct(x.ratio, 1)}</span></div>`).join("") : '<div class="empty">暂无持仓暴露</div>'}
      <div class="panel-head" style="margin-top:20px"><h2>拦截分布</h2><span class="tag">按规则</span></div>
      ${riskStatBars(logs)}
    </div>
  </div>

  <div class="panel" style="margin-top:18px">
    <div class="panel-head"><h2>风控规则</h2><span class="tag">阈值变更 >20% 需双人复核</span></div>
    ${tableHTML(["规则", "维度", "阈值", "动作", "状态", "版本", "待审批", "操作"], rules.map((r) => [
      `<span class="sym">${esc(r.rule_id)}</span><br><span style="color:var(--mut);font-size:11px">${esc(r.name)}</span>`,
      r.dimension, fmtTh({ threshold: r.threshold, dimension: r.dimension }), esc(r.action),
      r.enabled ? '<span class="badge ok">启用</span>' : '<span class="badge off">停用</span>',
      "v" + r.version,
      r.pending_threshold != null ? `<span class="badge pending">${r.pending_threshold}（${esc(r.pending_by || "")}）</span>` : "—",
      `<div class="actions">
        ${rw ? `<button class="btn tiny" data-act="editRule" data-id="${r.rule_id}">改阈值</button>` : ""}
        ${rw ? `<button class="btn tiny" data-act="toggleRule" data-id="${r.rule_id}" data-on="${r.enabled}">${r.enabled ? "停用" : "启用"}</button>` : ""}
        ${ap && r.pending_threshold != null ? `<button class="btn tiny" data-act="approveRule" data-id="${r.rule_id}">复核</button>` : ""}
      </div>`,
    ]))}
  </div>

  <div class="panel" style="margin-top:18px">
    <div class="panel-head"><h2>拦截审计</h2><span class="tag">最近 20 条</span></div>
    ${tableHTML(["时间", "规则", "维度", "动作", "原因"], logs.map((l) => [
      l.time, `<span class="sym">${esc(l.rule_id)}</span>`, esc(l.dimension),
      `<span class="badge ${l.action === "HALT" ? "rejected" : "pending"}">${esc(l.action)}</span>`,
      `<span style="white-space:normal">${esc(l.reason)}</span>`,
    ]))}
  </div>`;
}

function riskStatBars(logs) {
  if (!logs.length) return '<div class="empty">暂无拦截记录</div>';
  const m = {};
  logs.forEach((l) => (m[l.rule_id] = (m[l.rule_id] || 0) + 1));
  const rows = Object.entries(m).sort((a, b) => b[1] - a[1]);
  const max = rows[0][1];
  return rows.map(([k, v]) => `
    <div class="ind-row"><span class="sym">${esc(k)}</span>
      <span class="ind-bar"><i style="width:${(v / max) * 100}%"></i></span>
      <span class="ind-val">${v}</span></div>`).join("");
}

async function editRule(id) {
  const rules = (await api("/risk/rules", { meta: true })).data || [];
  const r = rules.find((x) => x.rule_id === id);
  if (!r) return;
  openModal(`修改规则 ${id}`, `
    <dl class="kv"><dt>名称</dt><dd>${esc(r.name)}</dd><dt>维度</dt><dd>${r.dimension}</dd>
      <dt>当前阈值</dt><dd>${r.threshold}</dd><dt>动作</dt><dd>${r.action}</dd></dl>
    <form class="form-grid" id="ruleForm">
      <label class="field"><span>新阈值</span><input name="threshold" type="number" step="0.001" value="${r.threshold}"></label>
      <label class="field"><span>变更理由</span><input name="reason" placeholder="必填" required></label>
      <div class="actions"><button class="btn primary" type="submit">提交</button></div>
      <div class="form-note">放宽幅度超过 20% 会进入待复核状态，需风控官（另一账号）审批后生效。</div>
    </form>`);
  $("#ruleForm").addEventListener("submit", async (e) => {
    e.preventDefault();
    const fd = new FormData(e.target);
    try {
      const res = await api(`/risk/rules/${id}`, {
        method: "PUT",
        body: { threshold: Number(fd.get("threshold")), reason: fd.get("reason") },
      });
      toast(res.status === "pending_approval" ? "已提交复核，等待风控官审批" : "规则已生效");
      closeModal();
      render();
    } catch (err) { toast(err.message, true); }
  });
}

/* ---------------- 07 策略 ---------------- */
async function vStrategies() {
  const list = (await api("/strategies", { meta: true })).data || [];
  S.cache.strategies = list;
  const sw = can("strategy:write"), bt = can("backtest:write");
  return `
  <div class="cards">
    ${list.map((s) => `
      <div class="card">
        <div class="l">${esc(s.id)}</div>
        <div class="v" style="font-size:17px">${esc(s.name)}</div>
        <div class="h">${esc(s.type)} · 权重 ${Number(s.weight).toFixed(2)} · 夏普 ${Number(s.sharpe).toFixed(2)}</div>
        <div style="margin-top:12px;display:flex;gap:6px;flex-wrap:wrap;align-items:center">
          <span class="badge ${s.status}">${({ on: "运行", off: "关闭", gray: "灰度", halt: "熔断" })[s.status] || s.status}</span>
          ${sw ? `<button class="btn tiny" data-act="stratSwitch" data-id="${s.id}">切换</button>` : ""}
          ${sw ? `<button class="btn tiny" data-act="stratParams" data-id="${s.id}">参数</button>` : ""}
          ${bt ? `<button class="btn tiny" data-act="stratBt" data-id="${s.id}">回测</button>` : ""}
        </div>
      </div>`).join("")}
  </div>

  <div class="panel" style="margin-top:18px">
    <div class="panel-head"><h2>参数与版本</h2><span class="tag">热更新需留痕，大改需复核</span></div>
    ${list.map((s) => `
      <div class="st-row">
        <div>
          <div class="st-name">${esc(s.name)} <span class="tag">v${s.param_version ?? 1}</span></div>
          <div class="st-sub">${Object.entries(s.params || {}).map(([k, v]) => `${k}=${v}`).join("  ") || "无参数"}</div>
        </div>
        <div class="actions">
          <button class="btn tiny" data-act="stratVersions" data-id="${s.id}">版本历史</button>
        </div>
      </div>`).join("")}
  </div>`;
}

async function strategyParams(id) {
  const d = await api(`/strategies/${id}`);
  const params = d.params || {};
  openModal(`策略参数 · ${id}`, `
    <form id="paramForm">
      <div class="form-grid">
        ${Object.entries(params).map(([k, v]) => `
          <label class="field"><span>${esc(k)}</span><input name="${esc(k)}" type="number" step="any" value="${v}"></label>`).join("")}
        <label class="field" style="grid-column:1/-1"><span>变更理由（必填）</span><input name="__reason" placeholder="例如：提升分散度" required></label>
      </div>
      <div class="actions" style="margin-top:14px"><button class="btn primary" type="submit">提交变更</button></div>
      <div class="form-note" style="margin-top:8px">单项变动超过 30% 会转待审批，需风控官复核后生效。</div>
    </form>`);
  $("#paramForm").addEventListener("submit", async (e) => {
    e.preventDefault();
    const fd = new FormData(e.target);
    const p = {};
    for (const [k, v] of fd.entries()) {
      if (k === "__reason") continue;
      p[k] = Number(v);
    }
    try {
      const res = await api(`/strategies/${id}/params`, {
        method: "PUT", body: { params: p, reason: fd.get("__reason") },
      });
      toast(res.status === "pending_approval" ? `v${res.version} 已提交复核` : `v${res.version} 已生效`);
      closeModal();
      render();
    } catch (err) { toast(err.message, true); }
  });
}

async function strategyVersions(id) {
  const vs = await api(`/strategies/${id}/versions`);
  const ap = can("approve");
  openModal(`参数版本 · ${id}`, tableHTML(["版本", "状态", "提交人", "理由", "参数", "操作"], vs.map((v) => [
    "v" + v.ver,
    `<span class="badge ${v.status === "active" ? "ok" : v.status === "pending" ? "pending" : "off"}">${v.status}</span>`,
    esc(v.author), esc(v.reason || ""),
    `<span style="white-space:normal;font-size:11px">${Object.entries(v.params || {}).map(([k, x]) => `${k}=${x}`).join(", ")}</span>`,
    v.status === "pending" && ap
      ? `<button class="btn tiny" data-act="approveVer" data-id="${id}" data-ver="${v.ver}">通过</button>
         <button class="btn tiny danger" data-act="rejectVer" data-id="${id}" data-ver="${v.ver}">驳回</button>`
      : "—",
  ])));
}

/* ---------------- 08 因子 ---------------- */
async function vFactors() {
  const cat = S.cache.factorCat || "";
  const res = await api(`/factors/meta?size=200&category=${encodeURIComponent(cat)}`, { meta: true });
  const list = (res.data && res.data.factors) || [];
  const byCat = (res.data && res.data.by_category) || {};
  // 实时因子值：取前 3 个标的，渲染前同步拉取（避免二次渲染闪烁）
  const live = {};
  for (const sym of (S.cache.symbols || []).slice(0, 3)) {
    try {
      const f = await api("/factors?symbol=" + sym);
      if (f && f.factors && Object.keys(f.factors).length) live[sym] = f.factors;
    } catch (e) { /* 忽略 */ }
  }
  const rw = can("factor:write");
  return `
  <div class="cards">
    ${Object.entries(byCat).map(([k, v]) => `<div class="card"><div class="l">${esc(k)}</div><div class="v">${v}</div><div class="h">个因子</div></div>`).join("")}
    <div class="card"><div class="l">在线因子</div><div class="v">${list.filter((f) => f.status === "online").length}</div><div class="h">可直接进模型</div></div>
  </div>

  <div class="panel" style="margin-top:18px">
    <div class="panel-head">
      <h2>因子库</h2>
      <div class="toolbar" style="margin:0">
        <select id="fcat">
          <option value="">全部类别</option>
          ${["量价", "基本面", "AI", "另类", "自定义"].map((c) => `<option ${c === cat ? "selected" : ""}>${c}</option>`).join("")}
        </select>
        ${rw ? `<button class="btn tiny" data-act="newFactor">注册因子</button>` : ""}
      </div>
    </div>
    ${tableHTML(["因子", "类别", "方向", "IC", "ICIR", "t值", "覆盖率", "状态", "操作"], list.map((f) => [
      `<span class="sym">${esc(f.id)}</span><br><span style="color:var(--mut);font-size:11px">${esc(f.name)}</span>`,
      esc(f.category), f.direction > 0 ? "正向" : "反向",
      `<span class="${cls(f.ic)}">${f.ic.toFixed(3)}</span>`, f.icir.toFixed(2), f.tstat.toFixed(1),
      pct(f.coverage, 0),
      `<span class="badge ${f.status === "online" ? "ok" : f.status === "testing" ? "pending" : "off"}">${f.status}</span>`,
      `<div class="actions">
        <button class="btn tiny" data-act="factorIC" data-id="${f.id}">IC 分析</button>
        ${rw ? `<button class="btn tiny" data-act="factorToggle" data-id="${f.id}" data-st="${f.status}">${f.status === "online" ? "下线" : "上线"}</button>` : ""}
        ${rw && !f.builtin ? `<button class="btn tiny danger" data-act="factorDel" data-id="${f.id}">删除</button>` : ""}
      </div>`,
    ]))}
  </div>

  <div class="panel" style="margin-top:18px">
    <div class="panel-head"><h2>实时因子值</h2><span class="tag">${Object.keys(live).length} 个标的</span></div>
    ${tableHTML(["标的", "因子", "值"], Object.entries(live).flatMap(([sym, fs]) =>
      Object.entries(fs).map(([k, v]) => [`<span class="sym">${sym}</span>`, `<span class="sym">${esc(k)}</span>`, Number(v).toFixed(4)])))}
  </div>`;
}

async function factorIC(id) {
  const r = await api(`/factors/${id}/ic?window=24`);
  openModal(`因子 IC · ${id}`, `
    <dl class="kv">
      <dt>IC 均值</dt><dd>${r.ic_mean.toFixed(4)}</dd>
      <dt>IC 标准差</dt><dd>${r.ic_std.toFixed(4)}</dd>
      <dt>ICIR</dt><dd>${r.icir.toFixed(3)}</dd>
      <dt>t 值</dt><dd>${r.tstat.toFixed(2)}</dd>
      <dt>IC 胜率</dt><dd>${pct(r.ic_win_rate, 1)}</dd>
      <dt>多空年化</dt><dd class="${cls(r.long_short)}">${signedPct(r.long_short)}</dd>
      <dt>结论</dt><dd style="font-family:inherit">${esc(r.conclusion)}</dd>
    </dl>
    <div class="panel-head"><h2>IC 序列</h2><span class="tag">${r.window}</span></div>
    <canvas id="icChart"></canvas>
    <div class="panel-head" style="margin-top:16px"><h2>10 分层年化收益</h2><span class="tag">D1 → D10</span></div>
    <canvas id="decChart"></canvas>`);
  drawLine($("#icChart"), r.series, { height: 150, labels: r.periods, fill: "rgba(159,184,196,.3)" });
  const { ctx, w, h } = prep($("#decChart"), 170);
  ctx.clearRect(0, 0, w, h);
  const vs = r.deciles, max = Math.max(...vs.map(Math.abs), 0.001);
  const bw = (w - 40) / vs.length;
  vs.forEach((v, i) => {
    const bh = (Math.abs(v) / max) * (h - 40);
    const x = 24 + i * bw + bw * 0.15;
    const y = v >= 0 ? h - 28 - bh : h - 28;
    ctx.fillStyle = v >= 0 ? "#c0392b" : "#1d7a4c";
    ctx.fillRect(x, y, bw * 0.7, Math.max(1, bh));
    ctx.fillStyle = "#b3b1a6";
    ctx.font = "9px monospace";
    ctx.fillText("D" + (i + 1), x, h - 10);
  });
}

/* ---------------- 09 回测 ---------------- */
async function vBacktests() {
  const jobs = (await api("/backtests?size=20", { meta: true })).data || [];
  const bt = can("backtest:write");
  return `
  <div class="panel">
    <div class="panel-head"><h2>发起回测</h2><span class="tag">异步任务 · 同参数可复现</span></div>
    ${bt ? `
    <form id="btForm" class="form-grid">
      <label class="field"><span>策略</span><select name="strategy">
        ${(S.cache.strategies || []).map((s) => `<option value="${s.id}">${s.id}</option>`).join("") || '<option value="alpha_mf">alpha_mf</option>'}
      </select></label>
      <label class="field"><span>开始日期</span><input name="start" type="date" value="2023-01-01"></label>
      <label class="field"><span>结束日期</span><input name="end" type="date" value="2024-12-31"></label>
      <div class="actions"><button class="btn primary" type="submit">开始回测</button></div>
    </form>` : `<div class="empty">当前角色无回测权限（需要 backtest:write）</div>`}
  </div>

  <div class="panel" style="margin-top:18px">
    <div class="panel-head"><h2>回测任务</h2><span class="tag">${jobs.length} 条</span></div>
    ${tableHTML(["任务", "策略", "区间", "状态", "进度", "夏普", "年化", "回撤", "耗时", "操作"], jobs.map((j) => [
      `<span class="sym">${esc(j.id)}</span>`, esc(j.strategy), `${esc(j.start)} ~ ${esc(j.end)}`,
      `<span class="badge ${j.status === "done" ? "ok" : j.status === "failed" ? "rejected" : "pending"}">${j.status}</span>`,
      j.progress + "%",
      j.result ? j.result.sharpe.toFixed(2) : "—",
      j.result ? signedPct(j.result.annual_return) : "—",
      j.result ? pct(j.result.max_drawdown) : "—",
      (j.duration_ms || 0) + "ms",
      `<button class="btn tiny" data-act="btDetail" data-id="${esc(j.id)}">结果</button>`,
    ]))}
  </div>`;
}

async function backtestDetail(id) {
  const j = await api(`/backtests/${id}`);
  if (!j.result) {
    openModal(`回测 ${id}`, `<div class="empty">任务${j.status}，进度 ${j.progress}%</div>`);
    return;
  }
  const r = j.result;
  openModal(`回测结果 · ${j.strategy} · ${id}`, `
    <div class="cards" style="margin-bottom:16px">
      <div class="card"><div class="l">累计收益</div><div class="v ${cls(r.total_return)}">${signedPct(r.total_return)}</div><div class="h">年化 ${signedPct(r.annual_return)}</div></div>
      <div class="card"><div class="l">夏普</div><div class="v">${r.sharpe.toFixed(2)}</div><div class="h">卡玛 ${r.calmar.toFixed(2)}</div></div>
      <div class="card"><div class="l">最大回撤</div><div class="v down">${pct(r.max_drawdown)}</div><div class="h">胜率 ${pct(r.win_rate, 1)}</div></div>
      <div class="card"><div class="l">超额收益</div><div class="v ${cls(r.excess_return)}">${signedPct(r.excess_return)}</div><div class="h">基准 ${signedPct(r.benchmark_return)}</div></div>
    </div>
    <canvas id="btChart"></canvas>
    <dl class="kv" style="margin-top:14px">
      <dt>交易日</dt><dd>${r.days} 天</dd>
      <dt>成交笔数</dt><dd>${r.trades}</dd>
      <dt>日均换手</dt><dd>${pct(r.turnover, 1)}</dd>
      <dt>盈亏比</dt><dd>${r.profit_factor.toFixed(2)}</dd>
    </dl>`);
  drawLine($("#btChart"), r.curve.map((x) => x.v), { height: 260, labels: r.curve.map((x) => x.t) });
}

/* ---------------- 10 报表 ---------------- */
async function vReports() {
  const daily = await api("/reports/daily");
  const attr = await api("/reports/attribution?dim=strategy");
  const attrInd = await api("/reports/attribution?dim=industry");
  const rows = (list) => (list || []).map((x) => [
    `<span class="sym">${esc(x.key)}</span>`,
    `<span class="${cls(x.pnl)}">${signedMoney(x.pnl)}</span>`,
    pct(x.contribution, 1), num(x.fills), money(x.turnover),
  ]);
  return `
  <div class="panel">
    <div class="panel-head">
      <h2>盘后日报 · ${daily.date}</h2>
      <div class="actions">
        <a class="btn tiny" href="${API}/reports/daily?format=csv" target="_blank" rel="noreferrer">导出 CSV</a>
        <a class="btn tiny" href="${API}/reports/attribution?dim=strategy&format=csv" target="_blank" rel="noreferrer">归因 CSV</a>
      </div>
    </div>
    <div class="cards">
      <div class="card"><div class="l">净值</div><div class="v">${daily.nav.toFixed(4)}</div><div class="h">总资产 ${money(daily.total_assets)}</div></div>
      <div class="card"><div class="l">当日盈亏</div><div class="v ${cls(daily.day_pnl)}">${signedMoney(daily.day_pnl)}</div><div class="h">${signedPct(daily.day_pnl_pct)}</div></div>
      <div class="card"><div class="l">已实现 / 浮动</div><div class="v" style="font-size:16px">${money(daily.realized_pnl)} / ${money(daily.unrealized_pnl)}</div><div class="h">现金流口径</div></div>
      <div class="card"><div class="l">风控拦截</div><div class="v">${daily.risk_rejects}</div><div class="h">${Object.entries(daily.risk_by_rule || {}).map(([k, v]) => k + ":" + v).join(" · ") || "无"}</div></div>
    </div>
  </div>

  <div class="grid-2">
    <div class="panel">
      <div class="panel-head"><h2>策略归因</h2><span class="tag">总盈亏 ${signedMoney(attr.total_pnl)}</span></div>
      ${tableHTML(["策略", "盈亏", "贡献", "成交", "成交额"], rows(attr.by_strategy))}
    </div>
    <div class="panel">
      <div class="panel-head"><h2>行业归因</h2><span class="tag">按市值加权</span></div>
      ${tableHTML(["行业", "盈亏", "贡献", "成交", "成交额"], rows(attrInd.by_industry))}
    </div>
  </div>

  <div class="panel" style="margin-top:18px">
    <div class="panel-head"><h2>标的归因</h2><span class="tag">现金流 + 期末市值</span></div>
    ${tableHTML(["标的", "盈亏", "贡献", "成交", "成交额"], rows(attr.by_symbol))}
  </div>`;
}

/* ---------------- 11 资金 ---------------- */
async function vCapital() {
  const c = await api("/capital");
  const cw = can("capital:write"), ap = can("approve");
  const trs = c.transfers || [];
  return `
  <div class="cards">
    <div class="card"><div class="l">可用资金</div><div class="v">${money(c.available)}</div><div class="h">可下单额度</div></div>
    <div class="card"><div class="l">冻结</div><div class="v">${money(c.frozen)}</div><div class="h">在途委托占用</div></div>
    <div class="card"><div class="l">总资产</div><div class="v">${money(c.total)}</div><div class="h">净值 ${c.nav.toFixed(4)}</div></div>
    <div class="card"><div class="l">已实现盈亏</div><div class="v ${cls(c.realized_pnl)}">${signedMoney(c.realized_pnl)}</div><div class="h">初始 ${money(c.initial)}</div></div>
  </div>

  <div class="panel" style="margin-top:18px">
    <div class="panel-head"><h2>出入金申请</h2><span class="tag">需双人复核</span></div>
    ${cw ? `
    <form id="trForm" class="form-grid">
      <label class="field"><span>类型</span><select name="type"><option value="deposit">入金</option><option value="withdraw">出金</option></select></label>
      <label class="field"><span>金额</span><input name="amount" type="number" value="5000000" step="10000" min="1"></label>
      <label class="field"><span>原因</span><input name="reason" placeholder="必填" required></label>
      <div class="actions"><button class="btn primary" type="submit">提交申请</button></div>
    </form>` : `<div class="empty">当前角色无资金操作权限</div>`}
  </div>

  <div class="panel" style="margin-top:18px">
    <div class="panel-head"><h2>划转流水</h2><span class="tag">${trs.filter((t) => t.status === "pending").length} 笔待审批</span></div>
    ${tableHTML(["单号", "类型", "金额", "状态", "申请人", "审批人", "原因", "操作"], trs.map((t) => [
      `<span class="sym">${esc(t.id)}</span>`,
      t.type === "deposit" ? "入金" : "出金", money(t.amount),
      `<span class="badge ${t.status === "approved" ? "ok" : t.status === "rejected" ? "rejected" : "pending"}">${t.status}</span>`,
      esc(t.requested_by), esc(t.approved_by || "—"),
      `<span style="white-space:normal">${esc(t.reason)}</span>`,
      t.status === "pending" && ap
        ? `<button class="btn tiny" data-act="approveTr" data-id="${t.id}" data-ok="1">通过</button>
           <button class="btn tiny danger" data-act="approveTr" data-id="${t.id}" data-ok="0">驳回</button>`
        : "—",
    ]))}
  </div>`;
}

/* ---------------- 12 审计 ---------------- */
async function vAudit() {
  const stats = await api("/audit/stats");
  const q = S.cache.auditQ || "";
  const { data, meta } = await api(`/audit/logs?size=40&q=${encodeURIComponent(q)}`, { meta: true });
  const logs = data || [];
  return `
  <div class="cards">
    <div class="card"><div class="l">审计总量</div><div class="v">${stats.total}</div><div class="h">全部写操作留痕</div></div>
    ${(stats.actions || []).slice(0, 3).map((a) => `<div class="card"><div class="l">${esc(a.action)}</div><div class="v">${a.count}</div><div class="h">次</div></div>`).join("")}
  </div>

  <div class="panel" style="margin-top:18px">
    <div class="panel-head">
      <h2>审计日志</h2>
      <div class="toolbar" style="margin:0">
        <input type="search" id="aq" placeholder="搜索用户 / 动作 / 资源" value="${esc(q)}">
        <span class="tag">${meta ? meta.total : 0} 条</span>
      </div>
    </div>
    ${tableHTML(["时间", "用户", "角色", "动作", "资源", "结果", "耗时", "IP"], logs.map((e) => [
      `<span class="sym">${esc(e.time)}</span>`, esc(e.user_name), `<span class="tag">${esc(e.role)}</span>`,
      `<span class="sym">${esc(e.action)}</span>`, `<span class="sym" style="white-space:normal">${esc(e.resource)}</span>`,
      `<span class="badge ${e.status < 300 ? "ok" : e.status < 500 ? "pending" : "rejected"}">${e.status}</span>`,
      (e.latency_ms || 0).toFixed(1) + "ms", `<span class="sym">${esc(e.ip)}</span>`,
    ]))}
  </div>

  <div class="panel" style="margin-top:18px">
    <div class="panel-head"><h2>动作分布</h2><span class="tag">按次数</span></div>
    ${(stats.actions || []).map((a) => `
      <div class="ind-row"><span class="sym" style="font-size:11px">${esc(a.action)}</span>
        <span class="ind-bar"><i style="width:${(a.count / (stats.actions[0].count || 1)) * 100}%"></i></span>
        <span class="ind-val">${a.count}</span></div>`).join("")}
  </div>`;
}

/* ---------------- 13 系统 ---------------- */
async function vSystem() {
  const cfg = await api("/system/config");
  const tasks = await api("/system/tasks");
  const users = await api("/system/users");
  const rw = can("risk:write"), sys = can("system:write");
  return `
  <div class="panel">
    <div class="panel-head"><h2>运行配置</h2><span class="tag">v${cfg.version} · ${cfg.env}</span></div>
    <div class="cards">
      <div class="card"><div class="l">运行时长</div><div class="v">${Math.floor(cfg.uptime_s / 60)} 分</div><div class="h">启动于 ${cfg.started_at}</div></div>
      <div class="card"><div class="l">交易日状态</div><div class="v" style="font-size:18px">${cfg.trade_status}</div><div class="h">${cfg.trade_date}</div></div>
      <div class="card"><div class="l">股票池</div><div class="v">${cfg.universe.length}</div><div class="h">${cfg.universe.join(" ")}</div></div>
      <div class="card"><div class="l">限流</div><div class="v" style="font-size:16px">${cfg.limits.read_rps} / ${cfg.limits.write_rps} rps</div><div class="h">读 / 写令牌桶</div></div>
    </div>
    <div class="actions" style="margin-top:16px">
      ${rw ? `<button class="btn danger" data-act="halt">一键全停</button>` : ""}
      ${rw ? `<button class="btn" data-act="resume">恢复交易</button>` : ""}
    </div>
  </div>

  <div class="panel" style="margin-top:18px">
    <div class="panel-head"><h2>定时任务</h2><span class="tag">盘前 / 盘中 / 盘后</span></div>
    ${tableHTML(["任务", "Cron", "状态", "上次结果", "耗时", "操作"], tasks.map((t) => [
      `<span class="sym">${esc(t.id)}</span><br><span style="color:var(--mut);font-size:11px">${esc(t.name)}</span>`,
      `<span class="sym">${esc(t.cron)}</span>`,
      `<span class="badge ${t.status === "success" ? "ok" : t.status === "running" ? "pending" : "off"}">${t.status}</span>`,
      `<span style="white-space:normal">${esc(t.last_result)}</span>`,
      (t.duration_ms || 0) + "ms",
      sys ? `<button class="btn tiny" data-act="runTask" data-id="${t.id}">执行</button>` : "—",
    ]))}
  </div>

  <div class="panel" style="margin-top:18px">
    <div class="panel-head"><h2>用户与权限矩阵</h2><span class="tag">RBAC</span></div>
    ${tableHTML(["用户", "角色", "说明", "权限"], (users.users || []).map((u) => [
      `<span class="sym">${esc(u.name)}</span>`, `<span class="tag">${esc(u.role)}</span>`, esc(u.descr),
      `<span style="white-space:normal;font-size:11px">${(users.roles[u.role] || []).join(" · ")}</span>`,
    ]))}
  </div>`;
}

/* ---------------- 14 对账 ---------------- */
async function vReconcile() {
  const r = await api("/reconcile");
  const diffs = r.diffs || [];
  return `
  <div class="recon">
    <div class="recon-card"><div class="l">对账结果</div><div class="v ${r.ok ? "recon-ok" : "recon-bad"}">${r.ok ? "一致" : diffs.length + " 处差异"}</div></div>
    <div class="recon-card"><div class="l">对账时间</div><div class="v">${r.time}</div></div>
    <div class="recon-card"><div class="l">差异标的</div><div class="v">${diffs.length}</div></div>
  </div>
  <div class="panel">
    <div class="panel-head">
      <h2>三账合一</h2>
      <div class="actions">
        <span class="tag">订单 / 成交 / 持仓</span>
        <button class="btn tiny" data-act="runRecon">立即对账</button>
      </div>
    </div>
    ${diffs.length ? tableHTML(["标的", "台账", "柜台", "差异"], diffs.map((d) => [
      `<span class="sym">${esc(d.symbol)}</span>`, num(d.expected), num(d.actual), `<span class="up">${num(d.delta)}</span>`,
    ])) : '<div class="empty">台账与柜台完全一致，无差异</div>'}
    <p class="form-note" style="margin-top:12px">口径：交易结果以券商柜台回执为唯一真值；Redis 做实时展示，MySQL 做落地对账，每日盘后自动校准。</p>
  </div>`;
}

/* ============================================================================
   渲染与路由
   ========================================================================== */
const VIEWS = {
  dashboard: [vDashboard, mountDashboard],
  positions: [vPositions],
  orders: [vOrders, mountOrders],
  fills: [vFills],
  signals: [vSignals],
  risk: [vRisk],
  strategies: [vStrategies],
  factors: [vFactors],
  backtests: [vBacktests],
  reports: [vReports],
  capital: [vCapital],
  audit: [vAudit],
  system: [vSystem],
  reconcile: [vReconcile],
};

let rendering = false;
async function render() {
  if (rendering) return;
  const def = VIEWS[S.view];
  if (!def) return;
  rendering = true;
  $("#pageTitle").textContent = TITLES[S.view] || S.view;
  try {
    const html = await def[0]();
    $("#view").innerHTML = html;
    if (def[1]) def[1]();
    bindViewEvents();
  } catch (e) {
    $("#view").innerHTML = `<div class="panel"><div class="empty">加载失败：${esc(e.message)}</div></div>`;
  } finally {
    rendering = false;
  }
}

function go(view) {
  if (!VIEWS[view]) view = "dashboard";
  S.view = view;
  S.page[view] = S.page[view] || 1;
  $$(".nav-item").forEach((el) => el.classList.toggle("active", el.dataset.view === view));
  render();
}

/* ---------------- 事件绑定（委托） ---------------- */
function bindViewEvents() {
  // 输入框里不自动刷新，避免打断用户
  document.addEventListener("focusin", () => (S.lastFocus = true), { once: true });
}

document.addEventListener("click", async (e) => {
  const nav = e.target.closest(".nav-item");
  if (nav) { e.preventDefault(); go(nav.dataset.view); return; }

  const el = e.target.closest("[data-act]");
  if (!el) return;
  const act = el.dataset.act;
  const id = el.dataset.id;
  try {
    switch (act) {
      case "page": {
        S.page[S.view] = Math.max(1, (S.page[S.view] || 1) + Number(el.dataset.dir));
        render();
        break;
      }
      case "orderDetail": await orderDetail(id); break;
      case "cancelOrder": {
        await api(`/orders/${encodeURIComponent(id)}`, { method: "DELETE" });
        toast("撤单成功");
        render();
        break;
      }
      case "batchCancel": {
        const r = await api("/orders/batch-cancel", { method: "POST", body: {} });
        toast(`批量撤单：成功 ${r.count} 张，失败 ${r.failed.length} 张`);
        render();
        break;
      }
      case "editRule": await editRule(id); break;
      case "toggleRule": {
        const on = el.dataset.on === "true";
        await api(`/risk/rules/${id}`, { method: "PUT", body: { enabled: !on } });
        toast(`规则 ${id} 已${on ? "停用" : "启用"}`);
        render();
        break;
      }
      case "approveRule": {
        await api(`/risk/rules/${id}/approve`, { method: "POST" });
        toast("复核通过，阈值已生效");
        render();
        break;
      }
      case "stratSwitch": {
        const cur = (S.cache.strategies || []).find((s) => s.id === id);
        const next = cur && cur.status === "on" ? "off" : "on";
        await api(`/strategies/${id}/switch`, { method: "POST", body: { status: next, reason: "工作台" } });
        toast(`${id} -> ${next}`);
        render();
        break;
      }
      case "stratParams": await strategyParams(id); break;
      case "stratVersions": await strategyVersions(id); break;
      case "stratBt": {
        const j = await api(`/strategies/${id}/backtest`, { method: "POST", body: {} });
        toast(`回测任务 ${j.id} 已创建`);
        go("backtests");
        break;
      }
      case "approveVer": {
        await api(`/strategies/${id}/versions/${el.dataset.ver}/approve`, { method: "POST" });
        toast("参数版本已生效");
        closeModal();
        render();
        break;
      }
      case "rejectVer": {
        await api(`/strategies/${id}/versions/${el.dataset.ver}/reject`, { method: "POST" });
        toast("已驳回");
        closeModal();
        render();
        break;
      }
      case "factorIC": await factorIC(id); break;
      case "factorToggle": {
        const next = el.dataset.st === "online" ? "offline" : "online";
        await api(`/factors/${id}`, { method: "PUT", body: { status: next } });
        toast(`因子 ${id} -> ${next}`);
        render();
        break;
      }
      case "factorDel": {
        await api(`/factors/${id}`, { method: "DELETE" });
        toast("因子已删除");
        render();
        break;
      }
      case "newFactor": await newFactor(); break;
      case "btDetail": await backtestDetail(id); break;
      case "approveTr": {
        await api(`/capital/transfers/${id}/approve`, { method: "POST", body: { approve: el.dataset.ok === "1" } });
        toast(el.dataset.ok === "1" ? "已通过" : "已驳回");
        render();
        break;
      }
      case "runTask": {
        const t = await api(`/system/tasks/${id}/run`, { method: "POST" });
        toast(`${id}：${t.last_result}`);
        render();
        break;
      }
      case "halt": {
        await api("/system/halt", { method: "POST", body: { reason: "工作台一键熔断" } });
        toast("已全停，所有策略进入熔断");
        render();
        break;
      }
      case "resume": {
        await api("/system/resume", { method: "POST" });
        toast("已恢复交易");
        render();
        break;
      }
      case "runRecon": {
        const r = await api("/reconcile/run", { method: "POST" });
        toast(r.ok ? "对账一致" : `发现 ${r.diffs.length} 处差异`, !r.ok);
        render();
        break;
      }
    }
  } catch (err) {
    toast(err.message, true);
  }
});

// 策略卡片里的快捷按钮
document.addEventListener("click", (e) => {
  const b = e.target.closest('[data-act="stratSwitch"], [data-act="stratParams"]');
  if (b) e.stopPropagation();
});

async function newFactor() {
  openModal("注册因子", `
    <form id="fForm" class="form-grid">
      <label class="field"><span>因子 ID（小写/数字/_）</span><input name="id" placeholder="turn_20d" required></label>
      <label class="field"><span>名称</span><input name="name" placeholder="20日换手" required></label>
      <label class="field"><span>类别</span><select name="category"><option>量价</option><option>基本面</option><option>AI</option><option>另类</option><option>自定义</option></select></label>
      <label class="field"><span>方向</span><select name="direction"><option value="1">正向</option><option value="-1">反向</option></select></label>
      <label class="field" style="grid-column:1/-1"><span>公式</span><input name="formula" placeholder="mean(turn,20)" required></label>
      <label class="field" style="grid-column:1/-1"><span>说明</span><input name="description" placeholder="可选"></label>
      <div class="actions"><button class="btn primary" type="submit">注册</button></div>
    </form>`);
  $("#fForm").addEventListener("submit", async (e) => {
    e.preventDefault();
    const fd = new FormData(e.target);
    try {
      await api("/factors", {
        method: "POST",
        body: {
          id: fd.get("id"), name: fd.get("name"), category: fd.get("category"),
          direction: Number(fd.get("direction")), formula: fd.get("formula"),
          description: fd.get("description"),
        },
      });
      toast("因子已注册（状态 testing）");
      closeModal();
      render();
    } catch (err) { toast(err.message, true); }
  });
}

/* ---------------- Modal ---------------- */
function openModal(title, html) {
  $("#modalTitle").textContent = title;
  $("#modalBody").innerHTML = html;
  $("#modal").hidden = false;
}
function closeModal() { $("#modal").hidden = true; $("#modalBody").innerHTML = ""; }
$("#modalClose").addEventListener("click", closeModal);
$("#modal").addEventListener("click", (e) => { if (e.target.id === "modal") closeModal(); });
document.addEventListener("keydown", (e) => { if (e.key === "Escape") closeModal(); });

/* ---------------- WebSocket 实时 ---------------- */
function connectWS() {
  if (!S.token) return;
  const proto = location.protocol === "https:" ? "wss" : "ws";
  const url = `${proto}://${location.host}${API}/ws?token=${encodeURIComponent(S.token)}&channels=overview,tick,risk`;
  try {
    const ws = new WebSocket(url);
    S.ws = ws;
    ws.onopen = () => { S.wsAlive = true; paintWS(); };
    ws.onclose = () => { S.wsAlive = false; paintWS(); setTimeout(() => { if (S.token) connectWS(); }, 3000); };
    ws.onerror = () => { S.wsAlive = false; paintWS(); };
    ws.onmessage = (ev) => {
      let m;
      try { m = JSON.parse(ev.data); } catch (e) { return; }
      if (m.type === "overview") {
        S.ov = m.data;
        paintTop(m.data);
        if (S.view === "dashboard") {
          const eq = (m.data.equity || []).map((x) => x.v);
          drawLine($("#spark"), eq, { height: 110, pad: false, fill: "rgba(233,161,120,.32)" });
          drawLine($("#equity"), eq, { height: 250, labels: (m.data.equity || []).map((x) => x.t) });
        }
      }
    };
  } catch (e) {
    setTimeout(connectWS, 3000);
  }
}

function paintWS() {
  const d = $("#wsDot");
  d.className = "dot" + (S.wsAlive ? " live" : "");
  $("#wsText").textContent = S.wsAlive ? "实时连接" : "重连中…";
}

function paintTop(ov) {
  $("#tradeDate").textContent = ov.trade_date;
  $("#accountId").textContent = ov.account_id;
  $("#navTop").textContent = ov.nav.toFixed(4);
  $("#latency").textContent = ov.latency_us;
  const map = { pre: "盘前", open: "盘中", mid: "午间", close: "盘后", halt: "熔断" };
  $("#tradeStatus").textContent = map[ov.status] || ov.status;
  $("#statusPill").textContent = map[ov.status] || ov.status;
}

/* ---------------- 启动 ---------------- */
$("#loginForm").addEventListener("submit", async (e) => {
  e.preventDefault();
  const btn = $("#loginBtn");
  btn.disabled = true;
  $("#loginErr").hidden = true;
  try {
    await doLogin($("#username").value.trim(), $("#password").value);
    boot();
  } catch (err) {
    $("#loginErr").hidden = false;
    $("#loginErr").textContent = err.message;
  } finally {
    btn.disabled = false;
  }
});

$$("#demoUsers button").forEach((b) => {
  b.addEventListener("click", () => {
    $("#username").value = b.dataset.u;
    $("#password").value = b.dataset.p;
  });
});

async function boot() {
  $("#login").hidden = true;
  $("#app").hidden = false;
  S.user = S.user || JSON.parse(localStorage.getItem("q.user") || "{}");
  S.perms = (S.perms && S.perms.length) ? S.perms : JSON.parse(localStorage.getItem("q.perms") || "[]");
  $("#userName").textContent = S.user.name || "—";
  $("#userRole").textContent = S.user.role || "—";
  paintWS();
  try {
    const ov = await api("/overview");
    S.ov = ov;
    paintTop(ov);
    const tick = await api("/market/tick");
    S.cache.symbols = (tick || []).map((t) => t.symbol);
    S.cache.strategies = await api("/strategies");
  } catch (e) { /* 忽略首帧失败 */ }
  go("dashboard");
  connectWS();
  setInterval(() => {
    if (document.activeElement && /INPUT|SELECT|TEXTAREA/.test(document.activeElement.tagName)) return;
    if (!$("#modal").hidden) return;
    if (["reports", "audit", "backtests"].includes(S.view)) render();
  }, 8000);
}

$("#refreshBtn").addEventListener("click", () => { render(); toast("已刷新"); });
$("#logoutBtn").addEventListener("click", () => logout(false));
window.addEventListener("resize", () => {
  if (S.ov && S.view === "dashboard") {
    const eq = (S.ov.equity || []).map((x) => x.v);
    drawLine($("#spark"), eq, { height: 110, pad: false, fill: "rgba(233,161,120,.32)" });
    drawLine($("#equity"), eq, { height: 250 });
  }
});

(async function init() {
  if (S.token && (await restore())) {
    boot();
  } else {
    $("#login").hidden = false;
    $("#app").hidden = true;
  }
})();
