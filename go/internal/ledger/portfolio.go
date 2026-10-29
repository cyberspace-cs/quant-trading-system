// Package ledger 资产台账层：强一致记账 + 盘后三账合一。
// 生产：database/sql + MySQL/TiDB，事务落库 order_main/fill_detail/position_snapshot/capital_account/risk_log。
// 演示：内存 Portfolio 实现同一套记账语义，口径与 SQL 表一致。
package ledger

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"quant/internal/common"
)

// RiskLog 风控审计记录（对应 risk_log 表）
type RiskLog struct {
	StrategyID string
	RuleID     string
	Dimension  string
	Action     string
	Reason     string
	At         int64
}

// Portfolio 内存台账，实现 common.Ledger
type Portfolio struct {
	mu        sync.RWMutex
	capital   common.Capital
	positions map[string]*common.Position
	orders    map[string]*common.Order
	fills     []*common.Fill
	riskLogs  []RiskLog
	realized  float64
}

func NewPortfolio(accountID string, cash float64) *Portfolio {
	return &Portfolio{
		capital:   common.Capital{AccountID: accountID, Available: cash, Total: cash, Nav: 1.0},
		positions: map[string]*common.Position{},
		orders:    map[string]*common.Order{},
	}
}

// ---- common.Ledger 接口实现 ----

func (p *Portfolio) SaveOrder(o *common.Order) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := *o
	p.orders[o.OrderID] = &cp
}

func (p *Portfolio) SaveFill(f *common.Fill) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fills = append(p.fills, f)
}

// ApplyFill 权威记账：以柜台成交回执为唯一真值更新持仓与资金。
func (p *Portfolio) ApplyFill(f *common.Fill) {
	p.mu.Lock()
	defer p.mu.Unlock()

	amount := f.FillPrice * float64(f.FilledQty)
	pos := p.positions[f.Symbol]
	if pos == nil {
		pos = &common.Position{Symbol: f.Symbol}
		p.positions[f.Symbol] = pos
	}

	if f.Side == common.Buy {
		newQty := pos.Qty + f.FilledQty
		if newQty > 0 {
			pos.AvgCost = (pos.AvgCost*float64(pos.Qty) + amount) / float64(newQty)
		}
		pos.Qty = newQty
		p.capital.Available -= amount
	} else {
		p.realized += (f.FillPrice - pos.AvgCost) * float64(f.FilledQty)
		pos.Qty -= f.FilledQty
		p.capital.Available += amount
		if pos.Qty <= 0 {
			pos.Qty = 0
			pos.AvgCost = 0
		}
	}
	pos.MarketValue = float64(pos.Qty) * pos.AvgCost
	p.recalcTotalLocked()
}

func (p *Portfolio) UpsertPosition(pos *common.Position) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := *pos
	p.positions[pos.Symbol] = &cp
	p.recalcTotalLocked()
}

func (p *Portfolio) SaveRiskLog(strategyID, ruleID, dimension, action, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.riskLogs = append(p.riskLogs, RiskLog{
		StrategyID: strategyID, RuleID: ruleID, Dimension: dimension,
		Action: action, Reason: reason, At: common.NowMs(),
	})
}

// recalcTotalLocked 总资产 = 可用资金 + 持仓市值（调用方持锁）
func (p *Portfolio) recalcTotalLocked() {
	mv := 0.0
	for _, pos := range p.positions {
		mv += pos.MarketValue
	}
	p.capital.Total = p.capital.Available + mv + p.capital.Frozen
	if p.capital.Total > 0 {
		p.capital.Nav = p.capital.Total / 100000000.0 // 初始净值基准
	}
}

// ---- 查询辅助（供风控 / 监控 / 对账）----

func (p *Portfolio) Positions() map[string]*common.Position {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]*common.Position, len(p.positions))
	for k, v := range p.positions {
		cp := *v
		out[k] = &cp
	}
	return out
}

func (p *Portfolio) Capital() common.Capital {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.capital
}

func (p *Portfolio) RealizedPnL() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.realized
}

func (p *Portfolio) OrderCount() int   { p.mu.RLock(); defer p.mu.RUnlock(); return len(p.orders) }
func (p *Portfolio) FillCount() int    { p.mu.RLock(); defer p.mu.RUnlock(); return len(p.fills) }
func (p *Portfolio) RiskLogCount() int { p.mu.RLock(); defer p.mu.RUnlock(); return len(p.riskLogs) }

// Snapshot 输出台账快照（对账 / 复盘用）
func (p *Portfolio) Snapshot() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var b strings.Builder
	fmt.Fprintf(&b, "账户=%s 可用=%.2f 冻结=%.2f 总资产=%.2f 净值=%.4f 已实现盈亏=%.2f\n",
		p.capital.AccountID, p.capital.Available, p.capital.Frozen, p.capital.Total, p.capital.Nav, p.realized)
	fmt.Fprintf(&b, "订单=%d 成交=%d 风控拦截=%d\n", len(p.orders), len(p.fills), len(p.riskLogs))
	syms := make([]string, 0, len(p.positions))
	for s := range p.positions {
		syms = append(syms, s)
	}
	sort.Strings(syms)
	for _, s := range syms {
		pos := p.positions[s]
		if pos.Qty == 0 {
			continue
		}
		fmt.Fprintf(&b, "  持仓 %s qty=%d avg=%.3f 市值=%.2f\n", s, pos.Qty, pos.AvgCost, pos.MarketValue)
	}
	return b.String()
}
