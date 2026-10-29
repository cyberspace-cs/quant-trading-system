package api

import (
	"fmt"
	"math"
	"strconv"
	"time"
)

// ===================== 策略接口 =====================

func (s *Server) hStrategies(c *Ctx) {
	list := s.State.StrategyList()
	out := make([]map[string]any, 0, len(list))
	for _, st := range list {
		params := s.State.S.Params(st.ID)
		out = append(out, map[string]any{
			"id": st.ID, "name": st.Name, "type": st.Type,
			"weight": st.Weight, "status": st.Status,
			"pnl": st.PnL, "sharpe": st.Sharpe,
			"params":        params,
			"param_version": s.activeVersion(st.ID),
		})
	}
	c.OK(out)
}

func (s *Server) activeVersion(id string) int {
	for _, v := range s.State.S.ParamVersions(id) {
		if v.Status == "active" {
			return v.Ver
		}
	}
	return 0
}

// strategyExists 策略是否注册在案
func (s *Server) strategyExists(id string) bool {
	for _, st := range s.State.StrategyList() {
		if st.ID == id {
			return true
		}
	}
	return false
}

func (s *Server) hStrategyDetail(c *Ctx) {
	id := c.Path("id")
	var found *StrategyInfo
	for _, st := range s.State.StrategyList() {
		if st.ID == id {
			found = st
			break
		}
	}
	if found == nil {
		c.NotFound("策略不存在：" + id)
		return
	}
	// 统计该策略的信号数与委托数
	sigCount, ordCount := 0, 0
	for _, sg := range s.State.SignalList(0) {
		if sg.Strategy == id {
			sigCount++
		}
	}
	for _, o := range s.State.Book.Orders() {
		if o.StrategyID == id {
			ordCount++
		}
	}
	c.OK(map[string]any{
		"strategy": found, "params": s.State.S.Params(id),
		"versions": s.State.S.ParamVersions(id),
		"stats": map[string]any{
			"signals": sigCount, "orders": ordCount,
			"param_version": s.activeVersion(id),
		},
	})
}

func (s *Server) hSwitchStrategy(c *Ctx) {
	id := c.Path("id")
	var req struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := c.Bind(&req); err != nil {
		c.Bad("请求体格式错误：" + err.Error())
		return
	}
	switch req.Status {
	case "on", "off", "gray", "halt":
	default:
		c.Bad("status 仅支持 on / off / gray / halt")
		return
	}
	if !s.State.SwitchStrategy(id, req.Status) {
		c.NotFound("策略不存在：" + id)
		return
	}
	c.Act("strategy.switch", "strategy:"+id, fmt.Sprintf("状态 -> %s（%s）", req.Status, req.Reason))
	c.OK(map[string]any{"ok": true, "id": id, "status": req.Status})
}

// hUpdateParams 参数热更新
//
// 风控要求：参数变更不能「随手改」，必须带理由、留版本；
// 单参数相对变动超过 30% 视为高风险，转待审批（需另一人复核）。
func (s *Server) hUpdateParams(c *Ctx) {
	id := c.Path("id")
	if !s.strategyExists(id) {
		c.NotFound("策略不存在：" + id)
		return
	}
	cur := s.State.S.Params(id)
	var req struct {
		Params map[string]float64 `json:"params"`
		Reason string             `json:"reason"`
	}
	if err := c.Bind(&req); err != nil {
		c.Bad("请求体格式错误：" + err.Error())
		return
	}
	if len(req.Params) == 0 {
		c.Bad("params 不能为空")
		return
	}
	if req.Reason == "" {
		c.Bad("必须填写变更理由（合规要求）")
		return
	}
	merged := map[string]float64{}
	for k, v := range cur {
		merged[k] = v
	}
	highRisk := false
	changed := []string{}
	for k, v := range req.Params {
		old, exists := merged[k]
		if exists && old != 0 && math.Abs(v-old)/math.Abs(old) > 0.3 {
			highRisk = true
		}
		if !exists || old != v {
			changed = append(changed, fmt.Sprintf("%s:%g->%g", k, old, v))
		}
		merged[k] = v
	}
	if len(changed) == 0 {
		c.Conflict("参数无变化")
		return
	}

	ver := s.activeVersion(id) + 1
	// 版本号取最大值 +1，避免与待审批版本冲突
	for _, v := range s.State.S.ParamVersions(id) {
		if v.Ver >= ver {
			ver = v.Ver + 1
		}
	}
	pv := &ParamVersion{
		Ver: ver, Params: merged, Author: c.User.Name, Reason: req.Reason,
		CreatedAt: time.Now().UnixMilli(),
		RiskLevel: "low", Status: "active", AppliedAt: time.Now().UnixMilli(),
	}
	if highRisk {
		pv.RiskLevel, pv.Status, pv.AppliedAt = "high", "pending", 0
	}
	s.State.S.AddParamVersion(id, pv)

	if highRisk {
		c.Act("strategy.params.request", "strategy:"+id,
			fmt.Sprintf("v%d 待审批（单项变动 >30%%）：%v", ver, changed))
		c.OK(map[string]any{
			"ok": true, "status": "pending_approval", "version": ver, "version_info": pv,
			"msg": "存在超过 30% 的参数变动，需风控官复核后生效",
		})
		return
	}
	// 低风险：立即切换为 active，旧版本置 superseded
	s.activateVersion(id, ver)
	c.Act("strategy.params.update", "strategy:"+id,
		fmt.Sprintf("v%d 生效：%v", ver, changed))
	c.OK(map[string]any{"ok": true, "status": "active", "version": ver, "params": merged})
}

// activateVersion 激活某版本（其余 active 置 superseded），并回调引擎热更新
func (s *Server) activateVersion(id string, ver int) bool {
	ok := false
	s.State.S.MutateParams(id, func(v *ParamVersion) {
		if v.Ver == ver {
			v.Status = "active"
			v.AppliedAt = time.Now().UnixMilli()
			ok = true
		} else if v.Status == "active" {
			v.Status = "superseded"
		}
	})
	if ok && s.OnParams != nil {
		s.OnParams(id, s.State.S.Params(id))
	}
	return ok
}

func (s *Server) hParamVersions(c *Ctx) {
	id := c.Path("id")
	vs := s.State.S.ParamVersions(id)
	if vs == nil {
		vs = []*ParamVersion{}
	}
	c.OK(vs)
}

func (s *Server) hApproveVersion(c *Ctx) {
	id := c.Path("id")
	ver, _ := strconv.Atoi(c.Path("ver"))
	vs := s.State.S.ParamVersions(id)
	var target *ParamVersion
	for _, v := range vs {
		if v.Ver == ver {
			target = v
		}
	}
	if target == nil {
		c.NotFound(fmt.Sprintf("版本 v%d 不存在", ver))
		return
	}
	if target.Status != "pending" {
		c.Conflict("该版本不是待审批状态：" + target.Status)
		return
	}
	if target.Author == c.User.Name {
		c.Forbidden("双人复核：参数提交人不能审批自己的版本")
		return
	}
	s.State.S.MutateParams(id, func(v *ParamVersion) {
		if v.Ver == ver {
			v.Approver = c.User.Name
		}
	})
	s.activateVersion(id, ver)
	c.Act("strategy.params.approve", fmt.Sprintf("strategy:%s:v%d", id, ver),
		fmt.Sprintf("复核通过 v%d（提交人 %s）", ver, target.Author))
	c.OK(map[string]any{"ok": true, "version": ver, "status": "active", "params": s.State.S.Params(id)})
}

func (s *Server) hRejectVersion(c *Ctx) {
	id := c.Path("id")
	ver, _ := strconv.Atoi(c.Path("ver"))
	ok := s.State.S.MutateParams(id, func(v *ParamVersion) {
		if v.Ver == ver && v.Status == "pending" {
			v.Status = "rejected"
			v.Approver = c.User.Name
		}
	})
	if !ok {
		c.NotFound(fmt.Sprintf("待审批版本 v%d 不存在", ver))
		return
	}
	c.Act("strategy.params.reject", fmt.Sprintf("strategy:%s:v%d", id, ver), "驳回参数版本")
	c.OK(map[string]any{"ok": true, "version": ver, "status": "rejected"})
}

// hStrategyBacktest 便捷入口：对某策略发起回测（等价 POST /backtests）
func (s *Server) hStrategyBacktest(c *Ctx) {
	id := c.Path("id")
	var req struct {
		Start  string             `json:"start"`
		End    string             `json:"end"`
		Params map[string]float64 `json:"params"`
	}
	_ = jsonBody(c.R, &req)
	job := s.startBacktest(id, req.Start, req.End, req.Params, c.User.Name)
	c.Act("backtest.create", "backtest:"+job.ID, "策略 "+id+" 回测任务")
	c.OK(job)
}
