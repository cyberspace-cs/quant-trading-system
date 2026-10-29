package api

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// ===================== 风控接口 =====================

func (s *Server) hRiskLogs(c *Ctx) {
	p := ParsePage(c, 50)
	all := s.State.Book.RiskLogs()
	rows := make([]RiskLogView, 0, len(all))
	for _, l := range all {
		if !Match(p.Filter, l.RuleID, l.Dimension, l.Reason) {
			continue
		}
		rows = append(rows, RiskLogView{
			RuleID: l.RuleID, Dimension: l.Dimension, Action: l.Action,
			Reason: l.Reason, Time: time.UnixMilli(l.At).Format("15:04:05"),
		})
	}
	page, meta := Slice(rows, p)
	c.OKPage(page, meta)
}

// hRiskRules 规则列表
func (s *Server) hRiskRules(c *Ctx) {
	rules := s.State.S.RulesList()
	p := ParsePage(c, 100)
	rows := make([]*RiskRuleRec, 0, len(rules))
	for _, r := range rules {
		if !Match(p.Filter, r.RuleID, r.Name, r.Dimension) {
			continue
		}
		rows = append(rows, r)
	}
	page, meta := Slice(rows, p)
	c.OKPage(page, meta)
}

// hCreateRule 新增规则
func (s *Server) hCreateRule(c *Ctx) {
	var req struct {
		RuleID      string  `json:"rule_id"`
		Name        string  `json:"name"`
		Dimension   string  `json:"dimension"`
		Threshold   float64 `json:"threshold"`
		Action      string  `json:"action"`
		Description string  `json:"description"`
	}
	if err := c.Bind(&req); err != nil {
		c.Bad("请求体格式错误：" + err.Error())
		return
	}
	if req.Name == "" || req.Dimension == "" {
		c.Bad("name 与 dimension 必填")
		return
	}
	req.Action = strings.ToUpper(req.Action)
	if req.Action == "" {
		req.Action = "REJECT"
	}
	if req.Action != "REJECT" && req.Action != "WARN" && req.Action != "HALT" {
		c.Bad("action 仅支持 REJECT / WARN / HALT")
		return
	}
	id := req.RuleID
	if id == "" {
		id = fmt.Sprintf("R_CUSTOM_%02d", s.State.S.NextSeq())
	}
	if s.State.S.Rule(id) != nil {
		c.Conflict("规则 ID 已存在：" + id)
		return
	}
	now := time.Now().UnixMilli()
	s.State.S.mu.Lock()
	s.State.S.Rules[id] = &RiskRuleRec{
		RuleID: id, Name: req.Name, Dimension: req.Dimension, Threshold: req.Threshold,
		Action: req.Action, Enabled: true, Version: 1, UpdatedAt: now,
		ApprovedBy: c.User.Name, Description: req.Description,
	}
	s.State.S.mu.Unlock()

	c.Act("risk.rule.create", "rule:"+id,
		fmt.Sprintf("新增规则 %s（%s %s %.4f）", req.Name, req.Dimension, req.Action, req.Threshold))
	c.OK(s.State.S.Rule(id))
}

// hUpdateRule 修改规则：小幅调整立即生效，大幅调整（>20%）转双人复核
func (s *Server) hUpdateRule(c *Ctx) {
	id := c.Path("id")
	old := s.State.S.Rule(id)
	if old == nil {
		c.NotFound("规则不存在：" + id)
		return
	}
	var req struct {
		Threshold *float64 `json:"threshold"`
		Action    string   `json:"action"`
		Enabled   *bool    `json:"enabled"`
		Reason    string   `json:"reason"`
	}
	if err := c.Bind(&req); err != nil {
		c.Bad("请求体格式错误：" + err.Error())
		return
	}
	now := time.Now().UnixMilli()

	// 启停：低风险，直接生效
	if req.Enabled != nil {
		s.State.S.MutateRule(id, func(r *RiskRuleRec) {
			r.Enabled = *req.Enabled
			r.UpdatedAt = now
			r.ApprovedBy = c.User.Name
		})
		c.Act("risk.rule.toggle", "rule:"+id, fmt.Sprintf("规则 %s 启用状态 -> %v", id, *req.Enabled))
		c.OK(s.State.S.Rule(id))
		return
	}

	if req.Action != "" {
		req.Action = strings.ToUpper(req.Action)
		if req.Action != "REJECT" && req.Action != "WARN" && req.Action != "HALT" {
			c.Bad("action 仅支持 REJECT / WARN / HALT")
			return
		}
	}

	if req.Threshold == nil && req.Action == "" {
		c.Bad("threshold / action / enabled 至少提供一个")
		return
	}

	// 判断是否需要复核：阈值放宽（绝对值变大）视为高风险，收紧视为低风险
	needApproval := false
	th := old.Threshold
	if req.Threshold != nil {
		th = *req.Threshold
		oldAbs, newAbs := math.Abs(old.Threshold), math.Abs(*req.Threshold)
		if oldAbs > 0 && newAbs > oldAbs*1.2 {
			needApproval = true
		}
	}
	if req.Action != "" && req.Action != old.Action && req.Action == "WARN" {
		needApproval = true // 把拦截降级为告警 = 放宽风险，必须复核
	}

	if needApproval {
		s.State.S.MutateRule(id, func(r *RiskRuleRec) {
			r.Pending = req.Threshold
			r.PendingBy = c.User.Name
			r.PendingAt = now
		})
		c.Act("risk.rule.change.request", "rule:"+id,
			fmt.Sprintf("提交规则变更待复核：%.4f -> %.4f（%s）", old.Threshold, th, req.Reason))
		c.OK(map[string]any{
			"ok": true, "status": "pending_approval", "rule": s.State.S.Rule(id),
			"msg": "阈值放宽幅度超过 20%，需风控官复核后生效",
		})
		return
	}

	s.State.S.MutateRule(id, func(r *RiskRuleRec) {
		r.Threshold = th
		if req.Action != "" {
			r.Action = req.Action
		}
		r.Version++
		r.UpdatedAt = now
		r.ApprovedBy = c.User.Name
		r.Pending = nil
	})
	s.applyRuleToEngine(id, th)
	c.Act("risk.rule.update", "rule:"+id,
		fmt.Sprintf("规则 %s 阈值 %.4f -> %.4f，即时生效", id, old.Threshold, th))
	c.OK(s.State.S.Rule(id))
}

// hApproveRule 复核通过
func (s *Server) hApproveRule(c *Ctx) {
	id := c.Path("id")
	r := s.State.S.Rule(id)
	if r == nil {
		c.NotFound("规则不存在：" + id)
		return
	}
	if r.Pending == nil {
		c.Conflict("该规则没有待复核的变更")
		return
	}
	if r.PendingBy == c.User.Name {
		c.Forbidden("双人复核：提交人不能审批自己的变更")
		return
	}
	th := *r.Pending
	s.State.S.MutateRule(id, func(x *RiskRuleRec) {
		x.Threshold = th
		x.Pending = nil
		x.Version++
		x.UpdatedAt = time.Now().UnixMilli()
		x.ApprovedBy = c.User.Name
	})
	s.applyRuleToEngine(id, th)
	c.Act("risk.rule.approve", "rule:"+id,
		fmt.Sprintf("复核通过 %s：阈值 -> %.4f（提交人 %s）", id, th, r.PendingBy))
	c.OK(s.State.S.Rule(id))
}

// hDeleteRule 停用规则（合规要求不物理删除，保留审计痕迹）
func (s *Server) hDeleteRule(c *Ctx) {
	id := c.Path("id")
	if s.State.S.Rule(id) == nil {
		c.NotFound("规则不存在：" + id)
		return
	}
	s.State.S.MutateRule(id, func(r *RiskRuleRec) {
		r.Enabled = false
		r.UpdatedAt = time.Now().UnixMilli()
	})
	c.Act("risk.rule.disable", "rule:"+id, "停用规则（软删除，保留审计）")
	c.OK(map[string]any{"ok": true, "rule_id": id, "enabled": false})
}

// applyRuleToEngine 规则变更后同步到事前风控引擎（否则改了规则不生效 = 假风控）
func (s *Server) applyRuleToEngine(ruleID string, th float64) {
	e := s.State.PreRisk
	switch ruleID {
	case "R_POS":
		e.MaxPositionRatio = th
	case "R_IND":
		e.MaxIndustryRatio = th
	case "R_LOSS":
		e.MaxDayLossRatio = th
	case "R_DD":
		e.MaxDrawdownRatio = th
	case "R_LOT":
		e.MaxLot = int64(th)
	case "R_TURN":
		e.MaxTurnover = int64(th)
	}
}

// LimitView 单维度额度占用
type LimitView struct {
	RuleID    string  `json:"rule_id"`
	Name      string  `json:"name"`
	Dimension string  `json:"dimension"`
	Threshold float64 `json:"threshold"`
	Current   float64 `json:"current"`
	UsagePct  float64 `json:"usage_pct"`
	Remaining float64 `json:"remaining"`
	Action    string  `json:"action"`
	Enabled   bool    `json:"enabled"`
	Status    string  `json:"status"` // ok / warn / breach
}

// hRiskLimits 各维度额度占用（风控台最核心的一块视图：还剩多少额度）
func (s *Server) hRiskLimits(c *Ctx) {
	snap := s.State.Snapshot()
	total := snap.Capital.Total
	if total <= 0 {
		total = 1
	}
	// 单票最大占用
	maxPos := 0.0
	for sym, p := range snap.Positions {
		px := snap.Prices[sym]
		if px == 0 {
			px = p.AvgCost
		}
		if r := float64(p.Qty) * px / total; r > maxPos {
			maxPos = r
		}
	}
	// 行业最大占用
	ind := map[string]float64{}
	for sym, p := range snap.Positions {
		px := snap.Prices[sym]
		if px == 0 {
			px = p.AvgCost
		}
		ind[s.State.Industry[sym]] += float64(p.Qty) * px
	}
	maxInd := 0.0
	for _, v := range ind {
		if r := v / total; r > maxInd {
			maxInd = r
		}
	}
	// 当日换手（委托笔数）
	turn := 0.0
	for _, o := range s.State.Book.Orders() {
		if o.ParentID == "" {
			turn++
		}
	}
	cur := map[string]float64{
		"position":  maxPos,
		"industry":  maxInd,
		"pnl":       snap.DayPnL / total,
		"drawdown":  s.State.Overview().MaxDrawdown,
		"lot":       s.maxLotToday(),
		"turnover":  turn,
		"limit":     0,
		"blacklist": 0,
		"custom":    0,
	}

	rules := s.State.S.RulesList()
	out := make([]LimitView, 0, len(rules))
	for _, r := range rules {
		cv := cur[r.Dimension]
		v := LimitView{
			RuleID: r.RuleID, Name: r.Name, Dimension: r.Dimension,
			Threshold: r.Threshold, Current: round4(cv), Action: r.Action, Enabled: r.Enabled,
		}
		// 阈值是负数的维度（亏损/回撤），用「已用比例」= current / threshold
		if r.Threshold != 0 {
			if r.Threshold < 0 {
				v.UsagePct = round4(cv / r.Threshold)
				v.Remaining = round4(r.Threshold - cv)
			} else {
				v.UsagePct = round4(cv / r.Threshold)
				v.Remaining = round4(r.Threshold - cv)
			}
		}
		switch {
		case !r.Enabled:
			v.Status = "disabled"
		case v.UsagePct >= 1:
			v.Status = "breach"
		case v.UsagePct >= 0.8:
			v.Status = "warn"
		default:
			v.Status = "ok"
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UsagePct > out[j].UsagePct })
	c.OK(map[string]any{
		"total_assets": round2(total), "limits": out,
		"industry_exposure": s.industryExposure(ind, total),
	})
}

func (s *Server) maxLotToday() float64 {
	max := int64(0)
	for _, o := range s.State.Book.Orders() {
		if o.OrigQty > max {
			max = o.OrigQty
		}
	}
	return float64(max)
}

func (s *Server) industryExposure(ind map[string]float64, total float64) []map[string]any {
	type kv struct {
		k string
		v float64
	}
	list := make([]kv, 0, len(ind))
	for k, v := range ind {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v })
	out := make([]map[string]any, 0, len(list))
	for _, x := range list {
		out = append(out, map[string]any{
			"industry": x.k, "value": round2(x.v), "ratio": round4(x.v / total),
		})
	}
	return out
}
