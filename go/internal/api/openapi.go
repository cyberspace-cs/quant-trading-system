package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ===================== OpenAPI 3.0 契约（契约先行）=====================
//
// 为什么要在后端直接吐 spec：
//   1) 前后端并行开发时，前端对着 /docs 就能自测，不用等后端联调；
//   2) CI 可以拉 spec 做 breaking-change 检测；
//   3) 面试时这是「工程规范」的直接证据，而不是口头说我有文档。

type apiDoc struct {
	Method  string
	Path    string
	Tag     string
	Summary string
	Perm    string
	Query   []string
	Body    string
}

var apiDocs = []apiDoc{
	{"GET", "/health", "系统", "健康检查与运行态", "public", nil, ""},
	{"POST", "/auth/login", "认证", "账号口令登录，签发 access + refresh", "public", nil, `{"username":"trader","password":"Trader@123"}`},
	{"POST", "/auth/refresh", "认证", "refresh token 轮换新 access token", "public", nil, `{"refresh_token":"..."}`},
	{"GET", "/auth/me", "认证", "当前用户、角色与权限清单", "read", nil, ""},
	{"POST", "/auth/logout", "认证", "登出并吊销当前令牌", "read", nil, ""},

	{"GET", "/overview", "账户", "总资产/净值/盈亏/回撤/夏普/换手/净值曲线", "read", nil, ""},
	{"GET", "/positions", "账户", "持仓（盯市市值与浮盈）", "read", []string{"q", "sort"}, ""},
	{"GET", "/performance", "账户", "绩效指标与净值曲线", "read", nil, ""},

	{"GET", "/orders", "交易", "订单列表（分页/排序/过滤）", "read", []string{"page", "size", "sort", "order", "q", "status", "symbol", "strategy"}, ""},
	{"GET", "/orders/{id}", "交易", "订单详情（含子单与成交明细）", "read", nil, ""},
	{"POST", "/orders", "交易", "手动下单：事前风控 → 拆单 → 柜台", "order:write", nil, `{"symbol":"600519","side":"BUY","qty":300,"price":1690,"algo":"TWAP","slices":3}`},
	{"PUT", "/orders/{id}", "交易", "改单（仅 SENT/PARTIAL）", "order:write", nil, `{"qty":200,"price":1688}`},
	{"DELETE", "/orders/{id}", "交易", "撤单", "order:write", nil, ""},
	{"POST", "/orders/batch-cancel", "交易", "批量撤单（按 ID 或按标的）", "order:write", nil, `{"order_ids":["ORD-1","ORD-2"]}`},

	{"GET", "/fills", "交易", "成交明细", "read", []string{"page", "size", "symbol"}, ""},
	{"GET", "/signals", "交易", "策略信号流水", "read", []string{"page", "size", "strategy"}, ""},

	{"GET", "/risk/logs", "风控", "风控拦截审计", "read", []string{"page", "size", "q"}, ""},
	{"GET", "/risk/rules", "风控", "风控规则与阈值", "read", nil, ""},
	{"POST", "/risk/rules", "风控", "新增规则（高风险变更需复核）", "risk:write", nil, `{"name":"次新股禁买","dimension":"custom","threshold":0,"action":"REJECT"}`},
	{"PUT", "/risk/rules/{id}", "风控", "变更规则阈值（进入待审批）", "risk:write", nil, `{"threshold":0.06}`},
	{"DELETE", "/risk/rules/{id}", "风控", "停用规则", "risk:write", nil, ""},
	{"POST", "/risk/rules/{id}/approve", "风控", "复核通过规则变更", "approve", nil, ""},
	{"GET", "/risk/limits", "风控", "各维度额度占用与剩余", "read", nil, ""},

	{"GET", "/strategies", "策略", "策略列表与运行态", "read", nil, ""},
	{"GET", "/strategies/{id}", "策略", "策略详情 + 当前参数", "read", nil, ""},
	{"POST", "/strategies/{id}/switch", "策略", "开关/灰度/熔断", "strategy:write", nil, `{"status":"on"}`},
	{"PUT", "/strategies/{id}/params", "策略", "参数热更新（大幅变更转待审批）", "strategy:write", nil, `{"params":{"top_n":5},"reason":"提高分散度"}`},
	{"GET", "/strategies/{id}/versions", "策略", "参数版本历史", "read", nil, ""},
	{"POST", "/strategies/{id}/versions/{ver}/approve", "策略", "审批通过参数版本", "approve", nil, ""},
	{"POST", "/strategies/{id}/versions/{ver}/reject", "策略", "驳回参数版本", "approve", nil, ""},
	{"POST", "/strategies/{id}/backtest", "策略", "对指定策略发起异步回测", "backtest:write", nil, `{"start":"2023-01-01","end":"2024-12-31","top_n":8}`},

	{"GET", "/factors", "因子", "实时因子值（?symbol=）或因子清单", "read", []string{"symbol"}, ""},
	{"GET", "/factors/meta", "因子", "因子库元数据（类别/IC/ICIR/覆盖率）", "read", []string{"category", "status", "q"}, ""},
	{"POST", "/factors", "因子", "注册自定义因子", "factor:write", nil, `{"id":"turn_20d","name":"20日换手","category":"量价","formula":"mean(turn,20)"}`},
	{"PUT", "/factors/{id}", "因子", "更新因子元数据", "factor:write", nil, `{"status":"online"}`},
	{"DELETE", "/factors/{id}", "因子", "下线/删除因子（内置因子禁删）", "factor:write", nil, ""},
	{"GET", "/factors/{id}/ic", "因子", "因子 IC 序列与分层收益", "read", []string{"window"}, ""},

	{"GET", "/market/tick", "行情", "全池最新行情", "read", nil, ""},
	{"GET", "/market/kline", "行情", "K 线聚合（1s/1m/5m）", "read", []string{"symbol", "period", "limit"}, ""},
	{"GET", "/market/depth", "行情", "五档盘口", "read", []string{"symbol"}, ""},
	{"GET", "/market/pool", "行情", "股票池/黑名单/涨跌停", "read", nil, ""},

	{"GET", "/backtests", "回测", "回测任务列表", "read", []string{"page", "size"}, ""},
	{"POST", "/backtests", "回测", "创建异步回测任务", "backtest:write", nil, `{"strategy":"alpha_mf","start":"2023-01-01","end":"2024-12-31"}`},
	{"GET", "/backtests/{id}", "回测", "回测结果与净值曲线", "read", nil, ""},

	{"GET", "/reports/daily", "报表", "盘后日报（?format=csv 导出）", "read", []string{"date", "format"}, ""},
	{"GET", "/reports/attribution", "报表", "收益归因：按策略/行业/标的", "read", []string{"dim", "format"}, ""},

	{"GET", "/reconcile", "对账", "三账合一当前结果", "read", nil, ""},
	{"POST", "/reconcile/run", "对账", "手动触发对账", "read", nil, ""},

	{"GET", "/capital", "资金", "资金账户与划转记录", "read", nil, ""},
	{"GET", "/capital/transfers", "资金", "划转流水", "read", []string{"status"}, ""},
	{"POST", "/capital/transfers", "资金", "发起出入金（待审批）", "capital:write", nil, `{"type":"deposit","amount":5000000,"reason":"追加资金"}`},
	{"POST", "/capital/transfers/{id}/approve", "资金", "审批出入金", "approve", nil, `{"approve":true}`},

	{"GET", "/audit/logs", "审计", "写操作审计日志（分页/过滤）", "read", []string{"page", "size", "user", "action", "q"}, ""},
	{"GET", "/audit/stats", "审计", "审计动作分布", "read", nil, ""},

	{"GET", "/system/config", "系统", "运行配置与特性开关", "read", nil, ""},
	{"GET", "/system/users", "系统", "用户与角色矩阵", "read", nil, ""},
	{"GET", "/system/tasks", "系统", "定时任务与下次执行时间", "read", nil, ""},
	{"POST", "/system/tasks/{id}/run", "系统", "手动触发定时任务", "system:write", nil, ""},
	{"POST", "/system/halt", "系统", "一键全停（极速熔断）", "risk:write", nil, `{"reason":"极端行情"}`},
	{"POST", "/system/resume", "系统", "恢复交易", "risk:write", nil, ""},

	{"GET", "/stream", "实时", "SSE 推送 overview/tick/order/risk", "read", nil, ""},
	{"GET", "/ws", "实时", "WebSocket 多频道订阅", "read", []string{"channels"}, ""},
}

// BuildOpenAPI 生成 OpenAPI 3.0 JSON
func BuildOpenAPI() []byte {
	paths := map[string]any{}
	tags := map[string]bool{}
	for _, d := range apiDocs {
		tags[d.Tag] = true
		item, _ := paths[d.Path].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[d.Path] = item
		}
		params := []any{}
		for _, q := range d.Query {
			params = append(params, map[string]any{
				"name": q, "in": "query", "required": false,
				"schema": map[string]any{"type": "string"},
			})
		}
		// 路径参数自动识别 :id / :ver
		for _, seg := range strings.Split(d.Path, "/") {
			if strings.HasPrefix(seg, "{") {
				name := strings.Trim(seg, "{}")
				params = append(params, map[string]any{
					"name": name, "in": "path", "required": true,
					"schema": map[string]any{"type": "string"},
				})
			}
		}
		op := map[string]any{
			"tags":        []string{d.Tag},
			"summary":     d.Summary,
			"operationId": strings.ToLower(d.Method) + strings.ReplaceAll(d.Path, "/", "_"),
			"security":    securityFor(d.Perm),
			"responses": map[string]any{
				"200": map[string]any{"description": "统一响应 envelope：{code,msg,data,meta,request_id,ts}"},
				"401": map[string]any{"description": "未认证"},
				"403": map[string]any{"description": "权限不足（需要 " + d.Perm + "）"},
				"429": map[string]any{"description": "限流"},
			},
			"x-permission": d.Perm,
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		if d.Body != "" {
			op["requestBody"] = map[string]any{
				"content": map[string]any{"application/json": map[string]any{
					"example": json.RawMessage(d.Body),
				}},
			}
		}
		item[strings.ToLower(d.Method)] = op
	}

	tagList := []any{}
	for t := range tags {
		tagList = append(tagList, map[string]any{"name": t})
	}
	spec := map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "QUANT 中高频量化交易系统 API",
			"version":     Version,
			"description": "五层架构（数据/策略/风控/执行/台账）对外接口。统一 envelope 响应，JWT + RBAC 鉴权，写接口要求 Idempotency-Key。",
		},
		"servers": []any{map[string]any{"url": "/api/v1"}},
		"tags":    tagList,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"bearerAuth": map[string]any{"type": "http", "scheme": "bearer", "bearerFormat": "JWT"},
			},
		},
		"paths": paths,
	}
	b, _ := json.MarshalIndent(spec, "", "  ")
	return b
}

func securityFor(perm string) []any {
	if perm == "public" {
		return []any{}
	}
	return []any{map[string]any{"bearerAuth": []any{}}}
}

// hDocs 自包含接口文档页（不依赖 CDN，内网可用）
func (s *Server) hDocs(c *Ctx) {
	type row struct{ M, P, T, S, Perm string }
	rows := make([]row, 0, len(apiDocs))
	for _, d := range apiDocs {
		rows = append(rows, row{d.Method, "/api/v1" + d.Path, d.Tag, d.Summary, d.Perm})
	}
	grouped := map[string][]row{}
	order := []string{}
	for _, r := range rows {
		if _, ok := grouped[r.T]; !ok {
			order = append(order, r.T)
		}
		grouped[r.T] = append(grouped[r.T], r)
	}

	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<title>QUANT API · 接口契约</title>
<style>
:root{--bg:#f5f4f0;--ink:#15150f;--mut:#8a877d;--line:#e6e3da;--buy:#c0392b;--sell:#1d7a4c}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);
font:14px/1.65 -apple-system,"PingFang SC","Noto Sans CJK SC",sans-serif}
.wrap{max-width:1080px;margin:0 auto;padding:56px 32px 96px}
h1{font:600 40px/1.2 "Songti SC",serif;margin:0 0 8px}
.sub{color:var(--mut);margin-bottom:40px}
.tag{font:600 12px/1 monospace;letter-spacing:.18em;color:var(--mut);
margin:36px 0 12px;text-transform:uppercase}
table{width:100%;border-collapse:collapse;background:#fff;border:1px solid var(--line)}
td,th{padding:11px 14px;border-bottom:1px solid var(--line);text-align:left;vertical-align:top}
th{background:#faf9f6;font-weight:600;font-size:12px;color:var(--mut);letter-spacing:.06em}
.m{font:700 11px/1 monospace;padding:4px 7px;border-radius:3px;color:#fff;display:inline-block}
.GET{background:#2f6f4e}.POST{background:#b8791f}.PUT{background:#2c5d8a}.DELETE{background:#a33a2c}
code{font-family:"DejaVu Sans Mono",monospace;font-size:13px}
.perm{font:11px monospace;color:var(--mut)}
tr:last-child td{border-bottom:0}
.note{margin-top:40px;padding:18px 20px;background:#fff;border:1px solid var(--line);color:var(--mut)}
.note b{color:var(--ink)}
</style></head><body><div class="wrap">
<h1>QUANT API 契约</h1>
<div class="sub">OpenAPI 3.0 规范：<a href="/api/v1/openapi.json">/api/v1/openapi.json</a> ·
统一响应 <code>{code,msg,data,meta,request_id,ts}</code> · 鉴权 <code>Authorization: Bearer &lt;jwt&gt;</code></div>`)

	for _, t := range order {
		fmt.Fprintf(&b, `<div class="tag">%s</div><table><tr><th style="width:80px">方法</th><th style="width:300px">路径</th><th>说明</th><th style="width:110px">权限</th></tr>`, t)
		for _, r := range grouped[t] {
			fmt.Fprintf(&b, `<tr><td><span class="m %s">%s</span></td><td><code>%s</code></td><td>%s</td><td class="perm">%s</td></tr>`,
				r.M, r.M, r.P, r.S, r.Perm)
		}
		b.WriteString(`</table>`)
	}
	b.WriteString(`<div class="note"><b>演示账号</b>　admin / Admin@123　trader / Trader@123　risk / Risk@123　research / Research@123　viewer / Viewer@123<br>
<b>写接口幂等</b>　请求头带 <code>Idempotency-Key: &lt;uuid&gt;</code>，24h 内重复请求回放首次结果（响应头 <code>X-Idempotent-Replay: true</code>）。</div>
</div></body></html>`)

	c.W.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.W.WriteHeader(http.StatusOK)
	_, _ = c.W.Write([]byte(b.String()))
}
