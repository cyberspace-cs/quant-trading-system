package api

import (
	"fmt"
	"sort"
	"time"
)

// ===================== 审计 / 资金 / 系统 =====================

func (s *Server) hAuditLogs(c *Ctx) {
	p := ParsePage(c, 50)
	q := AuditQuery{
		User:   c.Query("user"),
		Action: c.Query("action"),
		Q:      p.Filter,
	}
	all := s.Audit.List(q)
	page, meta := Slice(all, p)
	c.OKPage(page, meta)
}

func (s *Server) hAuditStats(c *Ctx) {
	stats := s.Audit.Stats()
	type kv struct {
		K string `json:"action"`
		V int    `json:"count"`
	}
	list := make([]kv, 0, len(stats))
	for k, v := range stats {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].V > list[j].V })
	c.OK(map[string]any{"total": s.Audit.Count(), "actions": list, "action_names": s.Audit.Actions()})
}

// ---------- 资金 ----------

func (s *Server) hCapital(c *Ctx) {
	cap := s.State.Book.Capital()
	ov := s.State.Overview()
	s.State.S.mu.RLock()
	trs := make([]*Transfer, len(s.State.S.Transfers))
	copy(trs, s.State.S.Transfers)
	s.State.S.mu.RUnlock()
	sort.Slice(trs, func(i, j int) bool { return trs[i].CreatedAt > trs[j].CreatedAt })
	pending := 0
	for _, t := range trs {
		if t.Status == "pending" {
			pending++
		}
	}
	c.OK(map[string]any{
		"account_id": cap.AccountID, "available": round2(cap.Available),
		"frozen": round2(cap.Frozen), "total": round2(ov.Total),
		"nav": ov.Nav, "initial": s.State.Initial,
		"realized_pnl":      round2(s.State.Book.RealizedPnL()),
		"pending_transfers": pending,
		"transfers":         trs,
	})
}

func (s *Server) hTransfers(c *Ctx) {
	status := c.Query("status")
	s.State.S.mu.RLock()
	out := make([]*Transfer, 0, len(s.State.S.Transfers))
	for _, t := range s.State.S.Transfers {
		if status != "" && t.Status != status {
			continue
		}
		cp := *t
		out = append(out, &cp)
	}
	s.State.S.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	c.OK(out)
}

func (s *Server) hCreateTransfer(c *Ctx) {
	var req struct {
		Type   string  `json:"type"`
		Amount float64 `json:"amount"`
		Reason string  `json:"reason"`
	}
	if err := c.Bind(&req); err != nil {
		c.Bad("请求体格式错误：" + err.Error())
		return
	}
	if req.Type != "deposit" && req.Type != "withdraw" {
		c.Bad("type 仅支持 deposit / withdraw")
		return
	}
	if req.Amount <= 0 {
		c.Bad("amount 必须大于 0")
		return
	}
	if req.Reason == "" {
		c.Bad("必须填写划转原因")
		return
	}
	if req.Type == "withdraw" && req.Amount > s.State.Book.Capital().Available {
		c.Conflict(fmt.Sprintf("可用资金不足：可用 %.2f，申请 %.2f", s.State.Book.Capital().Available, req.Amount))
		return
	}
	now := time.Now().UnixMilli()
	tr := &Transfer{
		ID:   fmt.Sprintf("TR-%s-%02d", time.Now().Format("150405"), s.State.S.NextSeq()),
		Type: req.Type, Amount: req.Amount, Status: "pending", Reason: req.Reason,
		RequestedBy: c.User.Name, CreatedAt: now, UpdatedAt: now,
	}
	s.State.S.mu.Lock()
	s.State.S.Transfers = append(s.State.S.Transfers, tr)
	s.State.S.mu.Unlock()

	c.Act("capital.transfer.request", "transfer:"+tr.ID,
		fmt.Sprintf("%s %.2f（%s）", req.Type, req.Amount, req.Reason))
	c.OK(tr)
}

func (s *Server) hApproveTransfer(c *Ctx) {
	id := c.Path("id")
	var req struct {
		Approve bool   `json:"approve"`
		Reason  string `json:"reason"`
	}
	_ = jsonBody(c.R, &req)

	s.State.S.mu.Lock()
	var target *Transfer
	for _, t := range s.State.S.Transfers {
		if t.ID == id {
			target = t
			break
		}
	}
	s.State.S.mu.Unlock()
	if target == nil {
		c.NotFound("划转单不存在：" + id)
		return
	}
	if target.Status != "pending" {
		c.Conflict("该划转单已处理：" + target.Status)
		return
	}
	if target.RequestedBy == c.User.Name {
		c.Forbidden("双人复核：申请人不能审批自己的划转")
		return
	}
	now := time.Now().UnixMilli()
	if !req.Approve {
		s.State.S.mu.Lock()
		target.Status = "rejected"
		target.ApprovedBy = c.User.Name
		target.UpdatedAt = now
		s.State.S.mu.Unlock()
		c.Act("capital.transfer.reject", "transfer:"+id, "驳回划转："+req.Reason)
		c.OK(target)
		return
	}
	delta := target.Amount
	if target.Type == "withdraw" {
		delta = -target.Amount
	}
	avail := s.State.Book.AdjustCash(delta)
	s.State.S.mu.Lock()
	target.Status = "approved"
	target.ApprovedBy = c.User.Name
	target.UpdatedAt = now
	s.State.S.mu.Unlock()

	c.Act("capital.transfer.approve", "transfer:"+id,
		fmt.Sprintf("通过 %s %.2f，可用资金 -> %.2f", target.Type, target.Amount, avail))
	c.OK(map[string]any{"ok": true, "transfer": target, "available": round2(avail)})
}

// ---------- 系统 ----------

func (s *Server) hSystemConfig(c *Ctx) {
	c.OK(map[string]any{
		"env": "sim", "version": Version, "build": "go" + "1.21",
		"uptime_s":     int(time.Since(s.StartAt).Seconds()),
		"trade_date":   s.State.TradeDate,
		"trade_status": s.State.TradeStatus,
		"account_id":   s.State.AccountID,
		"universe":     s.State.SymbolsCopy(),
		"features": map[string]bool{
			"jwt_auth": true, "rbac": true, "rate_limit": true, "idempotency": true,
			"websocket": true, "sse": true, "audit": true, "openapi": true,
			"dual_approval": true,
		},
		"limits": map[string]any{
			"read_rps": 80, "write_rps": 8, "max_page_size": 500,
			"jwt_ttl_min": 45, "idempotency_ttl_h": 24,
		},
		"middleware": map[string]string{
			"cache": "memory (生产: Redis Cluster)",
			"bus":   "memory (生产: Kafka)",
			"db":    "memory (生产: MySQL/TiDB)",
			"tsdb":  "memory (生产: ClickHouse)",
		},
		"started_at": s.StartAt.Format("2006-01-02 15:04:05"),
	})
}

func (s *Server) hSystemUsers(c *Ctx) {
	users := s.Auth.Users()
	sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })
	matrix := map[string][]string{}
	for _, role := range []string{"admin", "trader", "risk", "researcher", "viewer"} {
		matrix[role] = PermsOf(role)
	}
	c.OK(map[string]any{"users": users, "roles": matrix})
}

func (s *Server) hSystemTasks(c *Ctx) {
	s.State.S.mu.RLock()
	out := make([]*SysTask, 0, len(s.State.S.Tasks))
	for _, t := range s.State.S.Tasks {
		cp := *t
		out = append(out, &cp)
	}
	s.State.S.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	c.OK(out)
}

func (s *Server) hRunTask(c *Ctx) {
	id := c.Path("id")
	s.State.S.mu.RLock()
	t, ok := s.State.S.Tasks[id]
	s.State.S.mu.RUnlock()
	if !ok {
		c.NotFound("任务不存在：" + id)
		return
	}
	begin := time.Now()
	result := ""
	switch id {
	case "T_PREOPEN":
		result = fmt.Sprintf("股票池 %d 只 · 黑名单 1 只 · 风控规则 %d 条已加载",
			len(s.State.SymbolsCopy()), len(s.State.S.RulesList()))
	case "T_RECON":
		r := s.State.Reconcile()
		if r.OK {
			result = "订单/成交/持仓 三账合一，零差异"
		} else {
			result = fmt.Sprintf("发现 %d 处差异，已告警", len(r.Diffs))
		}
	case "T_WARMUP":
		result = fmt.Sprintf("预热 %d 只标的 · %d 个因子", len(s.State.SymbolsCopy()), len(s.State.S.FactorList()))
	default:
		result = "执行完成"
	}
	s.State.S.mu.Lock()
	t.Status = "success"
	t.LastRun = commonNow()
	t.NextRun = commonNow() + 3600_000
	t.LastResult = result
	t.DurationMs = time.Since(begin).Milliseconds()
	s.State.S.mu.Unlock()

	c.Act("system.task.run", "task:"+id, "手动触发："+result)
	c.OK(t)
}

// commonNow 统一毫秒时间（避免 import 冲突）
func commonNow() int64 { return time.Now().UnixMilli() }

// hHalt 一键全停：所有策略置熔断 + 交易状态 halt，并通知上层停止下单
func (s *Server) hHalt(c *Ctx) {
	var req struct {
		Reason string `json:"reason"`
	}
	_ = jsonBody(c.R, &req)
	if req.Reason == "" {
		req.Reason = "手动熔断"
	}
	for _, st := range s.State.StrategyList() {
		s.State.SwitchStrategy(st.ID, "halt")
	}
	s.State.SetStatus("halt")
	if s.OnHalt != nil {
		s.OnHalt(true, req.Reason)
	}
	c.Act("system.halt", "account:"+s.State.AccountID, "一键全停："+req.Reason)
	c.OK(map[string]any{"ok": true, "status": "halt", "reason": req.Reason})
}

func (s *Server) hResume(c *Ctx) {
	for _, st := range s.State.StrategyList() {
		if st.Status == "halt" {
			s.State.SwitchStrategy(st.ID, "on")
		}
	}
	s.State.SetStatus("open")
	if s.OnHalt != nil {
		s.OnHalt(false, "手动恢复")
	}
	c.Act("system.resume", "account:"+s.State.AccountID, "恢复交易")
	c.OK(map[string]any{"ok": true, "status": "open"})
}
