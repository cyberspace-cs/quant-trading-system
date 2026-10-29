package strategy

import (
	"quant/internal/common"
	"quant/internal/data"
)

// Generator 将打分结果转化为标准化调仓信号，并做防抖与多策略聚合。
type Generator struct {
	engine      *MultiFactorEngine
	TopN        int
	PerPosRatio float64 // 单票目标市值占账户总资产比例
	DebounceMs  int64   // 同一标的+方向 防抖窗口，降低换手磨损
	lastSignal  map[string]int64
}

func NewGenerator(engine *MultiFactorEngine, topN int, perPosRatio float64, debounceMs int64) *Generator {
	return &Generator{
		engine:      engine,
		TopN:        topN,
		PerPosRatio: perPosRatio,
		DebounceMs:  debounceMs,
		lastSignal:  map[string]int64{},
	}
}

// Generate 生成调仓信号：
//  1. 全池打分排序，取 TopN 为目标组合
//  2. 目标内未持有 -> 买入（等权 sizing，按 100 股整手）
//  3. 持有但已跌出目标 -> 卖出
func (g *Generator) Generate(
	fc *data.FactorCache,
	symbols []string,
	totalCapital float64,
	positions map[string]*common.Position,
	prices map[string]float64,
	now int64,
) []common.Signal {
	ranked := g.engine.Rank(fc, symbols)
	if len(ranked) == 0 {
		return nil
	}
	if g.TopN > len(ranked) {
		g.TopN = len(ranked)
	}
	target := map[string]bool{}
	for i := 0; i < g.TopN; i++ {
		target[ranked[i].Symbol] = true
	}

	var sigs []common.Signal

	// 买入 / 加仓
	for i := 0; i < g.TopN; i++ {
		sym := ranked[i].Symbol
		pos := positions[sym]
		held := int64(0)
		if pos != nil {
			held = pos.Qty
		}
		px := prices[sym]
		if px <= 0 {
			continue
		}
		targetValue := totalCapital * g.PerPosRatio
		targetQty := int64(targetValue/px/100) * 100 // 整手
		delta := targetQty - held
		if delta < 100 {
			continue
		}
		if g.debounce(sym+"BUY", now) {
			continue
		}
		sigs = append(sigs, common.Signal{
			StrategyID: "alpha_mf", Symbol: sym, Side: common.Buy,
			Qty: delta, Price: px, Ts: now,
		})
	}

	// 卖出（跌出目标）
	for sym, pos := range positions {
		if pos == nil || pos.Qty <= 0 {
			continue
		}
		if target[sym] {
			continue
		}
		if g.debounce(sym+"SELL", now) {
			continue
		}
		px := prices[sym]
		sigs = append(sigs, common.Signal{
			StrategyID: "alpha_mf", Symbol: sym, Side: common.Sell,
			Qty: pos.Qty, Price: px, Ts: now,
		})
	}
	return sigs
}

func (g *Generator) debounce(key string, now int64) bool {
	if last, ok := g.lastSignal[key]; ok && now-last < g.DebounceMs {
		return true
	}
	g.lastSignal[key] = now
	return false
}

// Aggregate 多策略信号聚合：同标的同方向合并数量（避免对手盘、降低冲击）。
func Aggregate(signals []common.Signal) []common.Signal {
	type k struct {
		sym  string
		side common.Side
	}
	idx := map[k]int{}
	var out []common.Signal
	for _, s := range signals {
		key := k{s.Symbol, s.Side}
		if i, ok := idx[key]; ok {
			out[i].Qty += s.Qty
			continue
		}
		idx[key] = len(out)
		out = append(out, s)
	}
	return out
}
