package api

import (
	"fmt"
	"net/http"
	"runtime"
	"time"
)

// ===================== 系统 / 认证 接口 =====================

func (s *Server) hHealth(c *Ctx) {
	c.OK(map[string]any{
		"status": "ok", "version": Version,
		"uptime_s":   int(time.Since(s.StartAt).Seconds()),
		"trade_date": s.State.TradeDate, "trade_status": s.State.TradeStatus,
		"env": "sim", "ts": time.Now().Format(time.RFC3339),
	})
}

// hLogin 账号登录，返回 access + refresh 令牌
func (s *Server) hLogin(c *Ctx) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.Bind(&req); err != nil {
		c.Bad("请求体格式错误：" + err.Error())
		return
	}
	if req.Username == "" || req.Password == "" {
		c.Bad("username / password 不能为空")
		return
	}
	res, err := s.Auth.Login(req.Username, req.Password)
	if err != nil {
		s.Audit.Add(AuditEntry{
			UserName: req.Username, Action: "auth.login.failed", Resource: "auth",
			Method: c.R.Method, Path: c.R.URL.Path, IP: clientIP(c.R),
			Status: http.StatusUnauthorized, RequestID: c.ReqID, Detail: "口令错误",
			Time: time.Now().Format("2006-01-02 15:04:05.000"), TS: time.Now().UnixMilli(),
		})
		c.Fail(http.StatusUnauthorized, CodeUnauthorized, "用户名或密码错误")
		return
	}
	c.Act("auth.login", "user:"+res.User.ID, "登录成功："+res.User.Name+" ("+res.Role+")")
	c.OK(res)
}

// hRefresh 刷新令牌（refresh 轮换）
func (s *Server) hRefresh(c *Ctx) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := c.Bind(&req); err != nil || req.RefreshToken == "" {
		c.Bad("refresh_token 不能为空")
		return
	}
	res, err := s.Auth.Refresh(req.RefreshToken)
	if err != nil {
		c.Fail(http.StatusUnauthorized, CodeUnauthorized, "refresh token 无效："+err.Error())
		return
	}
	c.Act("auth.refresh", "user:"+res.User.ID, "令牌轮换")
	c.OK(res)
}

// hMe 当前登录用户 + 权限
func (s *Server) hMe(c *Ctx) {
	u := s.Auth.UserByID(c.User.Sub)
	if u == nil {
		c.NotFound("用户不存在")
		return
	}
	c.OK(map[string]any{
		"user": u, "perms": PermsOf(u.Role), "role": u.Role,
		"token_exp": c.User.Exp, "jti": c.User.Jti,
	})
}

// hLogout 登出（令牌加入黑名单）
func (s *Server) hLogout(c *Ctx) {
	s.Auth.Revoke(c.User.Jti, c.User.Exp)
	c.Act("auth.logout", "user:"+c.User.Sub, "登出并吊销令牌")
	c.OK(map[string]any{"ok": true})
}

// hMetrics Prometheus 文本指标
func (s *Server) hMetrics(c *Ctx) {
	ov := s.State.Overview()
	cap := s.State.Book.Capital()
	w := c.W
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	line := func(name, typ string, val any, help string) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %v\n", name, help, name, typ, name, val)
	}
	line("quant_nav", "gauge", ov.Nav, "账户净值")
	line("quant_total_assets", "gauge", ov.Total, "总资产（盯市）")
	line("quant_available_cash", "gauge", cap.Available, "可用资金")
	line("quant_day_pnl", "gauge", ov.DayPnL, "当日盈亏")
	line("quant_max_drawdown", "gauge", ov.MaxDrawdown, "最大回撤")
	line("quant_sharpe", "gauge", ov.Sharpe, "夏普比率")
	line("quant_turnover", "gauge", ov.Turnover, "换手率")
	line("quant_latency_us", "gauge", ov.LatencyUs, "下单延迟（微秒）")
	line("quant_positions", "gauge", ov.Positions, "持仓标的数")
	line("quant_orders_total", "counter", ov.Orders, "订单总数")
	line("quant_fills_total", "counter", ov.Fills, "成交总数")
	line("quant_risk_rejects_total", "counter", ov.RiskRejects, "风控拦截总数")
	line("quant_audit_entries", "gauge", s.Audit.Count(), "审计日志条数")

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	line("go_goroutines", "gauge", runtime.NumGoroutine(), "goroutine 数")
	line("go_heap_alloc_bytes", "gauge", ms.HeapAlloc, "堆内存占用")
}

// hOpenAPI 输出 OpenAPI 3.0 规范（支持 ?format=yaml 的简化版不实现，统一 JSON）
func (s *Server) hOpenAPI(c *Ctx) {
	c.W.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.W.WriteHeader(http.StatusOK)
	_, _ = c.W.Write(BuildOpenAPI())
}
