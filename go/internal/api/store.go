package api

import (
	"sort"
	"sync"
	"time"

	"quant/internal/risk"
)

// ===================== 可变业务存储 =====================
//
// 这些是「可写」的域对象：策略参数版本、风控规则、因子库、资金划转、定时任务、回测任务。
// 生产环境全部落 MySQL（表结构见 sql/01_schema.sql），并用 Redis 缓存热数据；
// 这里用带读写锁的内存实现，保证接口语义、并发安全与演示可跑通。

// ParamVersion 策略参数版本（参数热更新需审批，故带版本与状态）
type ParamVersion struct {
	Ver       int                `json:"ver"`
	Params    map[string]float64 `json:"params"`
	Status    string             `json:"status"` // pending/active/rejected/superseded
	Author    string             `json:"author"`
	Approver  string             `json:"approver,omitempty"`
	Reason    string             `json:"reason"`
	RiskLevel string             `json:"risk_level"` // low/high（high 需双人复核）
	CreatedAt int64              `json:"created_at"`
	AppliedAt int64              `json:"applied_at,omitempty"`
}

// RiskRuleRec 风控规则（支持变更审批：pending 阈值 + 当前生效阈值）
type RiskRuleRec struct {
	RuleID      string   `json:"rule_id"`
	Name        string   `json:"name"`
	Dimension   string   `json:"dimension"`
	Threshold   float64  `json:"threshold"`
	Action      string   `json:"action"` // REJECT/WARN/HALT
	Enabled     bool     `json:"enabled"`
	Version     int      `json:"version"`
	Pending     *float64 `json:"pending_threshold,omitempty"`
	PendingBy   string   `json:"pending_by,omitempty"`
	PendingAt   int64    `json:"pending_at,omitempty"`
	ApprovedBy  string   `json:"approved_by,omitempty"`
	UpdatedAt   int64    `json:"updated_at"`
	Description string   `json:"description"`
}

// FactorMeta 因子元数据（因子工厂的「目录」，含 IC 评价）
type FactorMeta struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Category    string  `json:"category"` // 量价/基本面/AI/另类
	Formula     string  `json:"formula"`
	Unit        string  `json:"unit"`
	Direction   int     `json:"direction"` // 1 正向 -1 反向
	Owner       string  `json:"owner"`
	Status      string  `json:"status"` // online/offline/testing
	Builtin     bool    `json:"builtin"`
	IC          float64 `json:"ic"`
	ICIR        float64 `json:"icir"`
	TStat       float64 `json:"tstat"`
	Coverage    float64 `json:"coverage"`
	Turnover    float64 `json:"turnover"`
	Description string  `json:"description"`
	CreatedAt   int64   `json:"created_at"`
	UpdatedAt   int64   `json:"updated_at"`
}

// Transfer 资金划转（出入金审批流）
type Transfer struct {
	ID          string  `json:"id"`
	Type        string  `json:"type"` // deposit/withdraw
	Amount      float64 `json:"amount"`
	Status      string  `json:"status"` // pending/approved/rejected
	Reason      string  `json:"reason"`
	RequestedBy string  `json:"requested_by"`
	ApprovedBy  string  `json:"approved_by,omitempty"`
	CreatedAt   int64   `json:"created_at"`
	UpdatedAt   int64   `json:"updated_at"`
}

// SysTask 定时任务（运维调度中心）
type SysTask struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Cron       string `json:"cron"`
	Status     string `json:"status"` // idle/running/success/failed
	LastRun    int64  `json:"last_run"`
	LastResult string `json:"last_result"`
	DurationMs int64  `json:"duration_ms"`
	NextRun    int64  `json:"next_run"`
}

// Stores 全部可写域对象
type Stores struct {
	mu        sync.RWMutex
	ParamVers map[string][]*ParamVersion
	Rules     map[string]*RiskRuleRec
	Factors   map[string]*FactorMeta
	Transfers []*Transfer
	Tasks     map[string]*SysTask
	Backtest  map[string]*BacktestJob
	seq       int
}

// NewStores 初始化并灌入默认数据（与 PreTradeEngine 默认阈值保持一致）
func NewStores(pre *risk.PreTradeEngine) *Stores {
	s := &Stores{
		ParamVers: map[string][]*ParamVersion{},
		Rules:     map[string]*RiskRuleRec{},
		Factors:   map[string]*FactorMeta{},
		Transfers: []*Transfer{},
		Tasks:     map[string]*SysTask{},
		Backtest:  map[string]*BacktestJob{},
	}
	now := time.Now().UnixMilli()
	// ---- 默认风控规则（六条核心）----
	rules := []*RiskRuleRec{
		{RuleID: "R_POS", Name: "单票仓位上限", Dimension: "position", Threshold: pre.MaxPositionRatio, Action: "REJECT", Enabled: true, Description: "单只股票市值 / 总资产，超限直接拦截"},
		{RuleID: "R_IND", Name: "单行业暴露上限", Dimension: "industry", Threshold: pre.MaxIndustryRatio, Action: "REJECT", Enabled: true, Description: "同一行业合计暴露，防风格集中"},
		{RuleID: "R_LOSS", Name: "单日最大亏损", Dimension: "pnl", Threshold: pre.MaxDayLossRatio, Action: "HALT", Enabled: true, Description: "当日亏损触及阈值，策略整体熔断"},
		{RuleID: "R_DD", Name: "最大回撤熔断", Dimension: "drawdown", Threshold: pre.MaxDrawdownRatio, Action: "HALT", Enabled: true, Description: "净值自峰值回撤触发熔断"},
		{RuleID: "R_LOT", Name: "单笔手数上限", Dimension: "lot", Threshold: float64(pre.MaxLot), Action: "REJECT", Enabled: true, Description: "单笔委托数量上限，防大单冲击"},
		{RuleID: "R_TURN", Name: "当日换手上限", Dimension: "turnover", Threshold: float64(pre.MaxTurnover), Action: "REJECT", Enabled: true, Description: "策略当日委托笔数上限"},
	}
	for _, r := range rules {
		r.Version, r.UpdatedAt = 1, now
		s.Rules[r.RuleID] = r
	}
	// ---- 内置因子库（8 个）----
	facs := []*FactorMeta{
		{ID: "momentum_20d", Name: "20日动量", Category: "量价", Formula: "close / delay(close,20) - 1", Direction: 1,
			IC: 0.041, ICIR: 0.62, TStat: 5.8, Coverage: 0.98, Turnover: 0.21, Owner: "research", Status: "online", Builtin: true,
			Description: "经典动量，捕获中期趋势；需做行业与市值中性化"},
		{ID: "rev_5d", Name: "5日反转", Category: "量价", Formula: "-(close / delay(close,5) - 1)", Direction: -1,
			IC: 0.052, ICIR: 0.88, TStat: 7.4, Coverage: 0.98, Turnover: 0.52, Owner: "research", Status: "online", Builtin: true,
			Description: "A股短周期反转显著，中高频主力因子"},
		{ID: "vol_20d", Name: "20日波动率", Category: "量价", Formula: "std(ret, 20) * sqrt(252)", Direction: -1,
			IC: 0.031, ICIR: 0.45, TStat: 4.1, Coverage: 0.97, Turnover: 0.12, Owner: "research", Status: "online", Builtin: true,
			Description: "低波动异象，风险因子兼选股因子"},
		{ID: "volume_ratio", Name: "量比", Category: "量价", Formula: "vol / mean(vol, 5)", Direction: 1,
			IC: 0.023, ICIR: 0.31, TStat: 3.2, Coverage: 0.99, Turnover: 0.68, Owner: "research", Status: "online", Builtin: true,
			Description: "量能异动，日内策略常用"},
		{ID: "bp_lf", Name: "账面市值比", Category: "基本面", Formula: "equity / market_cap", Direction: 1,
			IC: 0.028, ICIR: 0.41, TStat: 3.9, Coverage: 0.93, Turnover: 0.04, Owner: "research", Status: "online", Builtin: true,
			Description: "价值因子，低频但稳定"},
		{ID: "roe_ttm", Name: "ROE(TTM)", Category: "基本面", Formula: "net_profit_ttm / equity", Direction: 1,
			IC: 0.035, ICIR: 0.52, TStat: 4.6, Coverage: 0.92, Turnover: 0.03, Owner: "research", Status: "online", Builtin: true,
			Description: "质量因子，盈利能力强"},
		{ID: "ai_resid_1", Name: "AI 残差特征", Category: "AI", Formula: "LSTM(seq60) 残差项", Direction: 1,
			IC: 0.061, ICIR: 1.12, TStat: 9.1, Coverage: 0.95, Turnover: 0.44, Owner: "research", Status: "testing", Builtin: true,
			Description: "时序深度模型残差，需每日滚动训练，样本外衰减监控"},
		{ID: "north_flow", Name: "北向资金流", Category: "另类", Formula: "sum(north_buy - north_sell, 5)", Direction: 1,
			IC: 0.019, ICIR: 0.24, TStat: 2.1, Coverage: 0.71, Turnover: 0.35, Owner: "research", Status: "online", Builtin: true,
			Description: "北向资金 5 日净流入，情绪代理变量"},
	}
	for _, f := range facs {
		f.CreatedAt, f.UpdatedAt = now, now
		s.Factors[f.ID] = f
	}
	// ---- 定时任务 ----
	tasks := []*SysTask{
		{ID: "T_PREOPEN", Name: "盘前初始化（股票池/黑名单/涨跌停/风控参数）", Cron: "0 9 * * 1-5", Status: "success", LastResult: "股票池 7 只 · 黑名单 1 只 · 涨跌停 2 只"},
		{ID: "T_WARMUP", Name: "行情预热与因子预计算", Cron: "15 9 * * 1-5", Status: "success", LastResult: "预热 7 只标的 · 3 个因子"},
		{ID: "T_MIDCHECK", Name: "午间风控巡检", Cron: "35 11 * * 1-5", Status: "idle", LastResult: "—"},
		{ID: "T_RECON", Name: "盘后三账合一对账", Cron: "5 15 * * 1-5", Status: "success", LastResult: "订单/成交/持仓 零差异"},
		{ID: "T_ARCHIVE", Name: "日终归档与日志备份", Cron: "0 16 * * 1-5", Status: "idle", LastResult: "—"},
		{ID: "T_RETRAIN", Name: "AI 模型滚动重训", Cron: "0 18 * * 1-5", Status: "idle", LastResult: "—"},
	}
	for _, t := range tasks {
		t.LastRun = now - 3600_000
		t.NextRun = now + 3600_000
		s.Tasks[t.ID] = t
	}
	return s
}

// NextSeq 生成业务序号
func (s *Stores) NextSeq() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq
}

// ---------- 策略参数 ----------

// Params 返回某策略当前生效参数
func (s *Stores) Params(id string) map[string]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := len(s.ParamVers[id]) - 1; i >= 0; i-- {
		if s.ParamVers[id][i].Status == "active" {
			return s.ParamVers[id][i].Params
		}
	}
	return map[string]float64{}
}

// ParamVersions 返回版本列表（新→旧）
func (s *Stores) ParamVersions(id string) []*ParamVersion {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.ParamVers[id]
	out := make([]*ParamVersion, len(src))
	for i, v := range src {
		cp := *v
		out[i] = &cp
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ver > out[j].Ver })
	return out
}

// AddParamVersion 追加版本
func (s *Stores) AddParamVersion(id string, v *ParamVersion) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ParamVers[id] = append(s.ParamVers[id], v)
}

// UpdateParamVersion 更新版本状态
func (s *Stores) UpdateParamVersion(id string, ver int, fn func(*ParamVersion) bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.ParamVers[id] {
		if v.Ver == ver {
			return fn(v)
		}
	}
	return false
}

// MutateParams 遍历某策略的全部版本做状态迁移（返回是否命中）
func (s *Stores) MutateParams(id string, fn func(*ParamVersion)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.ParamVers[id]
	if len(list) == 0 {
		return false
	}
	for _, v := range list {
		fn(v)
	}
	return true
}

// ---------- 风控规则 ----------

// RulesList 规则列表（按 ID 排序，稳定输出）
func (s *Stores) RulesList() []*RiskRuleRec {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*RiskRuleRec, 0, len(s.Rules))
	for _, r := range s.Rules {
		cp := *r
		if r.Pending != nil {
			p := *r.Pending
			cp.Pending = &p
		}
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RuleID < out[j].RuleID })
	return out
}

// Rule 单条规则
func (s *Stores) Rule(id string) *RiskRuleRec {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.Rules[id]
	if !ok {
		return nil
	}
	cp := *r
	return &cp
}

// MutateRule 修改规则
func (s *Stores) MutateRule(id string, fn func(*RiskRuleRec)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.Rules[id]
	if !ok {
		return false
	}
	fn(r)
	return true
}

// ---------- 因子库 ----------

// FactorList 因子列表
func (s *Stores) FactorList() []*FactorMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*FactorMeta, 0, len(s.Factors))
	for _, f := range s.Factors {
		cp := *f
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Factor 单因子
func (s *Stores) Factor(id string) *FactorMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok := s.Factors[id]
	if !ok {
		return nil
	}
	cp := *f
	return &cp
}

// PutFactor 新增/覆盖
func (s *Stores) PutFactor(f *FactorMeta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Factors[f.ID] = f
}

// DelFactor 删除（内置因子禁止删除，由上层校验）
func (s *Stores) DelFactor(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.Factors[id]; !ok {
		return false
	}
	delete(s.Factors, id)
	return true
}
