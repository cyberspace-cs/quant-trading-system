// Package api 对外 HTTP / SSE / WebSocket 接口层。
//
// 设计要点（对标工业级私募交易系统）：
//   - 统一响应契约 + 业务错误码 + request_id 全链路追踪
//   - JWT（HS256 手写）+ RBAC 五角色九权限，写接口强制鉴权
//   - 令牌桶限流（读写分开），防策略异常刷单打挂柜台
//   - 写接口幂等键（Idempotency-Key），网络重试不重复下单
//   - 所有写操作审计留痕（谁/何时/何地/做了什么/结果）
//   - 分页/排序/过滤统一由 ParsePage + Slice 处理，便于前端复用
package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Version 服务版本
const Version = "2.0.0"

type route struct {
	method string
	segs   []string
	perm   string // public 表示免鉴权
	h      func(*Ctx)
}

// Server HTTP 服务
type Server struct {
	State  *AppState
	WebDir string
	Auth   *Auth
	Audit  *AuditLog
	Idem   *IdemStore
	RLRead *Limiter
	RLWrit *Limiter

	StartAt time.Time
	reqSeq  int64
	ordSeq  int64
	routes  []route

	// 回调节点：参数生效 / 一键熔断时通知上层引擎（cmd/server 装配）
	OnParams func(strategyID string, params map[string]float64)
	OnHalt   func(halt bool, reason string)
}

// NewServer 构建服务并注册全部路由
func NewServer(st *AppState, webDir string) *Server {
	s := &Server{
		State: st, WebDir: webDir,
		Auth:    NewAuth("quant-dev-secret-change-in-production"),
		Audit:   st.Audit,
		Idem:    NewIdemStore(24 * time.Hour),
		RLRead:  NewLimiter(80, 160),
		RLWrit:  NewLimiter(8, 16),
		StartAt: time.Now(),
	}
	s.register()
	return s
}

// SetSecret 覆盖 JWT 密钥（生产从 KMS / 环境变量注入）
func (s *Server) SetSecret(sec string) { s.Auth = NewAuth(sec) }

// add 注册路由；path 形如 "/orders/:id"
func (s *Server) add(method, path, perm string, h func(*Ctx)) {
	segs := []string{}
	if p := strings.Trim(path, "/"); p != "" {
		segs = strings.Split(p, "/")
	}
	s.routes = append(s.routes, route{method: method, segs: segs, perm: perm, h: h})
}

// register 全部路由表 —— 这是「接口清单」的唯一真值来源，OpenAPI 也从这里生成。
func (s *Server) register() {
	const pub = "public"

	// ---------- 系统 / 认证 ----------
	s.add("GET", "/health", pub, s.hHealth)
	s.add("GET", "/openapi.json", pub, s.hOpenAPI)
	s.add("GET", "/docs", pub, s.hDocs)
	s.add("GET", "/metrics", pub, s.hMetrics)
	s.add("POST", "/auth/login", pub, s.hLogin)
	s.add("POST", "/auth/refresh", pub, s.hRefresh)
	s.add("GET", "/auth/me", PermRead, s.hMe)
	s.add("POST", "/auth/logout", PermRead, s.hLogout)

	// ---------- 账户总览 / 持仓 ----------
	s.add("GET", "/overview", PermRead, s.hOverview)
	s.add("GET", "/positions", PermRead, s.hPositions)
	s.add("GET", "/performance", PermRead, s.hPerformance)

	// ---------- 订单（含写操作）----------
	s.add("GET", "/orders", PermRead, s.hOrders)
	s.add("GET", "/orders/:id", PermRead, s.hOrderDetail)
	s.add("POST", "/orders", PermOrderWrite, s.hCreateOrder)
	s.add("PUT", "/orders/:id", PermOrderWrite, s.hAmendOrder)
	s.add("DELETE", "/orders/:id", PermOrderWrite, s.hCancelOrder)
	s.add("POST", "/orders/batch-cancel", PermOrderWrite, s.hBatchCancel)

	// ---------- 成交 / 信号 ----------
	s.add("GET", "/fills", PermRead, s.hFills)
	s.add("GET", "/signals", PermRead, s.hSignals)

	// ---------- 风控 ----------
	s.add("GET", "/risk/logs", PermRead, s.hRiskLogs)
	s.add("GET", "/risk/rules", PermRead, s.hRiskRules)
	s.add("POST", "/risk/rules", PermRiskWrite, s.hCreateRule)
	s.add("PUT", "/risk/rules/:id", PermRiskWrite, s.hUpdateRule)
	s.add("DELETE", "/risk/rules/:id", PermRiskWrite, s.hDeleteRule)
	s.add("POST", "/risk/rules/:id/approve", PermApprove, s.hApproveRule)
	s.add("GET", "/risk/limits", PermRead, s.hRiskLimits)

	// ---------- 策略 ----------
	s.add("GET", "/strategies", PermRead, s.hStrategies)
	s.add("GET", "/strategies/:id", PermRead, s.hStrategyDetail)
	s.add("POST", "/strategies/:id/switch", PermStratWrite, s.hSwitchStrategy)
	s.add("PUT", "/strategies/:id/params", PermStratWrite, s.hUpdateParams)
	s.add("GET", "/strategies/:id/versions", PermRead, s.hParamVersions)
	s.add("POST", "/strategies/:id/versions/:ver/approve", PermApprove, s.hApproveVersion)
	s.add("POST", "/strategies/:id/versions/:ver/reject", PermApprove, s.hRejectVersion)
	s.add("POST", "/strategies/:id/backtest", PermBacktest, s.hStrategyBacktest)

	// ---------- 因子 ----------
	s.add("GET", "/factors", PermRead, s.hFactors) // 兼容旧：?symbol= 返回实时因子值
	s.add("GET", "/factors/meta", PermRead, s.hFactorMeta)
	s.add("POST", "/factors", PermFactor, s.hCreateFactor)
	s.add("PUT", "/factors/:id", PermFactor, s.hUpdateFactor)
	s.add("DELETE", "/factors/:id", PermFactor, s.hDeleteFactor)
	s.add("GET", "/factors/:id/ic", PermRead, s.hFactorIC)

	// ---------- 行情 ----------
	s.add("GET", "/market/tick", PermRead, s.hMarketTick)
	s.add("GET", "/market/kline", PermRead, s.hKline)
	s.add("GET", "/market/depth", PermRead, s.hDepth)
	s.add("GET", "/market/pool", PermRead, s.hPool)

	// ---------- 回测 ----------
	s.add("GET", "/backtests", PermRead, s.hBacktests)
	s.add("POST", "/backtests", PermBacktest, s.hCreateBacktest)
	s.add("GET", "/backtests/:id", PermRead, s.hBacktestDetail)

	// ---------- 报表 ----------
	s.add("GET", "/reports/daily", PermRead, s.hDailyReport)
	s.add("GET", "/reports/attribution", PermRead, s.hAttribution)

	// ---------- 对账 ----------
	s.add("GET", "/reconcile", PermRead, s.hReconcile)
	s.add("POST", "/reconcile/run", PermRead, s.hReconcileRun)

	// ---------- 资金 ----------
	s.add("GET", "/capital", PermRead, s.hCapital)
	s.add("GET", "/capital/transfers", PermRead, s.hTransfers)
	s.add("POST", "/capital/transfers", PermCapital, s.hCreateTransfer)
	s.add("POST", "/capital/transfers/:id/approve", PermApprove, s.hApproveTransfer)

	// ---------- 审计 ----------
	s.add("GET", "/audit/logs", PermRead, s.hAuditLogs)
	s.add("GET", "/audit/stats", PermRead, s.hAuditStats)

	// ---------- 系统 ----------
	s.add("GET", "/system/config", PermRead, s.hSystemConfig)
	s.add("GET", "/system/users", PermRead, s.hSystemUsers)
	s.add("GET", "/system/tasks", PermRead, s.hSystemTasks)
	s.add("POST", "/system/tasks/:id/run", PermSystem, s.hRunTask)
	s.add("POST", "/system/halt", PermRiskWrite, s.hHalt)
	s.add("POST", "/system/resume", PermRiskWrite, s.hResume)

	// ---------- 实时 ----------
	s.add("GET", "/stream", PermRead, s.hStream) // SSE
	s.add("GET", "/ws", PermRead, s.hWS)         // WebSocket
}

// Handler 兼容旧调用
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.ServeHTTP) }

// respWriter 捕获状态码与响应体（供幂等回放与审计）
type respWriter struct {
	http.ResponseWriter
	status  int
	buf     *bytes.Buffer
	capture bool
	wrote   bool
}

func (w *respWriter) WriteHeader(code int) {
	if w.wrote {
		return
	}
	w.status = code
	w.wrote = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *respWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	if w.capture && w.buf != nil {
		w.buf.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *respWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *respWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("hijack unsupported")
}

// ServeHTTP 主入口：request_id → CORS → 静态 → 路由 → 鉴权 → 限流 → 幂等 → 审计
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	reqID := fmt.Sprintf("req-%d-%d", start.Unix()%100000, atomic.AddInt64(&s.reqSeq, 1))

	h := w.Header()
	h.Set("X-Request-Id", reqID)
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type,Authorization,Idempotency-Key,X-Request-Id")
	h.Set("Access-Control-Expose-Headers", "X-Request-Id,X-Total-Count,X-RateLimit-Remaining,X-Idempotent-Replay")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// 静态资源 / 前端工作台
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		http.FileServer(http.Dir(s.WebDir)).ServeHTTP(w, r)
		return
	}

	// 路由匹配
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	path = strings.Trim(path, "/")
	var segs []string
	if path != "" {
		segs = strings.Split(path, "/")
	}
	var hit *route
	var params map[string]string
	for i := range s.routes {
		rt := &s.routes[i]
		if rt.method != r.Method || len(rt.segs) != len(segs) {
			continue
		}
		ok := true
		p := map[string]string{}
		for j, ps := range rt.segs {
			if strings.HasPrefix(ps, ":") {
				p[ps[1:]] = segs[j]
				continue
			}
			if ps != segs[j] {
				ok = false
				break
			}
		}
		if ok {
			hit, params = rt, p
			break
		}
	}

	rw := &respWriter{ResponseWriter: w, status: http.StatusOK}
	ctx := &Ctx{W: rw, R: r, P: params, ReqID: reqID}

	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[panic] %s %s: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())
			if !rw.wrote {
				ctx.Fail(http.StatusInternalServerError, CodeInternal, "internal error")
			}
		}
	}()

	if hit == nil {
		ctx.NotFound("no route for " + r.Method + " " + r.URL.Path)
		return
	}

	// 鉴权：WS 走 query token（浏览器 WebSocket 不能自定义 header）
	if hit.perm != "public" {
		token := bearer(r)
		if token == "" && strings.HasSuffix(r.URL.Path, "/ws") {
			token = r.URL.Query().Get("token")
		}
		claims, err := s.Auth.Verify(token)
		if err != nil {
			ctx.Fail(http.StatusUnauthorized, CodeUnauthorized, "未登录或令牌已过期："+err.Error())
			return
		}
		ctx.User = claims
		if !Can(claims.Role, hit.perm) {
			ctx.Fail(http.StatusForbidden, CodeForbidden,
				fmt.Sprintf("角色 %s 无权限 %s", claims.Role, hit.perm))
			return
		}
	}

	// 限流：读/写分开
	rl := s.RLRead
	if r.Method != http.MethodGet {
		rl = s.RLWrit
	}
	key := "ip:" + clientIP(r)
	if ctx.User != nil {
		key = "u:" + ctx.User.Sub
	}
	if r.URL.Path != "/api/v1/metrics" && !rl.Allow(key) {
		w.Header().Set("Retry-After", "1")
		ctx.Fail(http.StatusTooManyRequests, CodeRateLimited, "请求过于频繁，请稍后重试")
		return
	}
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(rl.Remaining(key)))

	// 幂等：写接口带 Idempotency-Key 时，命中则直接回放首次响应
	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey != "" && r.Method != http.MethodGet {
		if body, status, ok := s.Idem.Get(ctx.User.Sub + "|" + idemKey); ok {
			w.Header().Set("X-Idempotent-Replay", "true")
			w.WriteHeader(status)
			_, _ = w.Write(body)
			return
		}
		rw.capture = true
		rw.buf = &bytes.Buffer{}
	}

	hit.h(ctx)

	// 落幂等缓存
	if idemKey != "" && r.Method != http.MethodGet && rw.buf != nil && rw.status < 300 {
		s.Idem.Put(ctx.User.Sub+"|"+idemKey, rw.status, rw.buf.Bytes())
	}

	// 审计：所有写操作 + 显式标记了 action 的读操作（如手动触发对账）
	if ctx.User != nil && (r.Method != http.MethodGet || ctx.Action != "") {
		act := ctx.Action
		if act == "" {
			act = strings.ToLower(r.Method) + " " + path
		}
		s.Audit.Add(AuditEntry{
			UserID: ctx.User.Sub, UserName: ctx.User.Name, Role: ctx.User.Role,
			Action: act, Resource: ctx.Resource, Method: r.Method, Path: r.URL.Path,
			IP: clientIP(r), Status: rw.status, Code: 0,
			LatencyMs: float64(time.Since(start).Microseconds()) / 1000.0,
			Detail:    ctx.Detail, RequestID: reqID,
		})
	}
}

// bearer 取 Authorization: Bearer xxx
func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return strings.TrimSpace(h)
}

// clientIP 取真实客户端 IP（穿透反向代理）
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return xr
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// jsonBody 快速读 JSON（给不需要严格字段校验的场景）
func jsonBody(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}
