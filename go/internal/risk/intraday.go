package risk

import (
	"encoding/json"
	"math"

	"quant/internal/common"
)

// IntradayMonitor 事中实时风控：毫秒级监控净值回撤、异常行情（闪崩/暴涨）。
type IntradayMonitor struct {
	cache  common.Cache
	ledger common.Ledger
	bus    common.EventBus

	dayPnL      float64
	peakPnL     float64
	maxDrawdown float64
	halted      bool
	lastPrice   map[string]float64
	crashThresh float64 // 单标的 5s 涨跌幅阈值，触发冻结
}

func NewIntradayMonitor(cache common.Cache, ledger common.Ledger, bus common.EventBus) *IntradayMonitor {
	return &IntradayMonitor{
		cache: cache, ledger: ledger, bus: bus,
		lastPrice:   map[string]float64{},
		crashThresh: 0.05,
	}
}

// UpdatePnL 刷新当日盈亏并更新回撤
func (m *IntradayMonitor) UpdatePnL(pnl float64) {
	m.dayPnL = pnl
	if pnl > m.peakPnL {
		m.peakPnL = pnl
	}
	if m.peakPnL > 0 {
		dd := (pnl - m.peakPnL) / m.peakPnL
		if dd < m.maxDrawdown {
			m.maxDrawdown = dd
		}
	}
	m.cache.Set("quant:risk:pnl:day", jsonNum(pnl))
}

// OnTick 异常行情熔断：单标的瞬时涨跌超阈值 -> 冻结标的池
func (m *IntradayMonitor) OnTick(t common.Tick) {
	prev, ok := m.lastPrice[t.Symbol]
	m.lastPrice[t.Symbol] = t.Last
	if !ok || prev == 0 {
		return
	}
	jump := (t.Last - prev) / prev
	if math.Abs(jump) > m.crashThresh {
		m.cache.Set("quant:risk:switch:crash", "halt")
		m.ledger.SaveRiskLog("", "R_CRASH", "market", "HALT",
			"异常行情熔断 "+t.Symbol)
		m.bus.Publish("risk.alert", []byte(`{"type":"crash","symbol":"`+t.Symbol+`"}`))
	}
}

// Drawdown 当前回撤
func (m *IntradayMonitor) Drawdown() float64 { return m.maxDrawdown }

// Halted 是否熔断
func (m *IntradayMonitor) Halted() bool {
	if m.halted {
		return true
	}
	v, ok := m.cache.Get("quant:risk:switch:crash")
	return ok && v == "halt"
}

func jsonNum(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}
