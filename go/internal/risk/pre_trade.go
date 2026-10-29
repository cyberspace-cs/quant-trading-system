// Package risk 矩阵式三层风控：事前拦截 / 事中监控 / 事后熔断。
// 事前风控：下单前 100% 拦截，违规订单直接丢弃、不进柜台。
package risk

import (
	"fmt"

	"quant/internal/common"
	"quant/internal/data"
)

// Context 事前风控所需上下文（实时持仓 / 资金 / 行业 / 价格）
type Context struct {
	Positions map[string]*common.Position
	Capital   *common.Capital
	Industry  map[string]string // symbol -> industry_code
	Prices    map[string]float64
	DayPnL    float64
}

// PreTradeEngine 事前风控引擎（规则预载，O(1) 判断）
type PreTradeEngine struct {
	cache  common.Cache
	ledger common.Ledger
	intIDs map[string]int

	MaxPositionRatio float64 // 单票最大仓位
	MaxIndustryRatio float64 // 单行业最大暴露
	MaxDayLossRatio  float64 // 单日最大亏损（负值，如 -0.02）
	MaxDrawdownRatio float64
	MaxLot           int64
	MaxTurnover      int64
}

func NewPreTradeEngine(cache common.Cache, ledger common.Ledger, intIDs map[string]int) *PreTradeEngine {
	return &PreTradeEngine{
		cache: cache, ledger: ledger, intIDs: intIDs,
		MaxPositionRatio: 0.08,
		MaxIndustryRatio: 0.30,
		MaxDayLossRatio:  -0.02,
		MaxDrawdownRatio: -0.03,
		MaxLot:           500000,
		MaxTurnover:      2000000,
	}
}

// Check 返回 (是否放行, 拒绝原因)。拒绝时写风控日志并落 Kafka 审计。
func (e *PreTradeEngine) Check(sig common.Signal, ctx *Context) (bool, string) {
	total := ctx.Capital.Total
	if total <= 0 {
		return e.reject(sig, "R_CAP", "capital", "REJECT", "账户总资产<=0")
	}

	// 1) 涨跌停过滤（Bitmap，O(1)）
	id, ok := e.intIDs[sig.Symbol]
	if ok {
		if sig.Side == common.Buy && e.cache.GetBit(data.KeyLimitUp, id) == 1 {
			return e.reject(sig, "R_LIMIT", "limit", "REJECT", "涨停禁止买入")
		}
		if sig.Side == common.Sell && e.cache.GetBit(data.KeyLimitDown, id) == 1 {
			return e.reject(sig, "R_LIMIT", "limit", "REJECT", "跌停禁止卖出")
		}
	}

	// 2) 黑名单 / ST / 停牌
	if e.cache.SIsMember(data.KeyBlacklist, sig.Symbol) {
		return e.reject(sig, "R_BL", "blacklist", "REJECT", "黑名单/ST/停牌")
	}

	// 3) 单笔手数上限
	if sig.Qty > e.MaxLot {
		return e.reject(sig, "R_LOT", "lot", "REJECT", fmt.Sprintf("单笔手数 %d 超限 %d", sig.Qty, e.MaxLot))
	}

	// 4) 单票仓位上限（仅买入校验）
	if sig.Side == common.Buy {
		notional := float64(sig.Qty) * sig.Price
		cur := 0.0
		if p := ctx.Positions[sig.Symbol]; p != nil {
			cur = float64(p.Qty) * sig.Price
		}
		if (cur+notional)/total > e.MaxPositionRatio {
			return e.reject(sig, "R_POS", "position", "REJECT",
				fmt.Sprintf("单票仓位 %.2f%% 超限 %.2f%%", (cur+notional)/total*100, e.MaxPositionRatio*100))
		}

		// 5) 行业暴露上限
		ind := ctx.Industry[sig.Symbol]
		indVal := notional
		for s, p := range ctx.Positions {
			if ctx.Industry[s] == ind && p != nil {
				px := ctx.Prices[s]
				if px == 0 {
					px = sig.Price
				}
				indVal += float64(p.Qty) * px
			}
		}
		if indVal/total > e.MaxIndustryRatio {
			return e.reject(sig, "R_IND", "industry", "REJECT",
				fmt.Sprintf("行业暴露 %.2f%% 超限 %.2f%%", indVal/total*100, e.MaxIndustryRatio*100))
		}
	}

	// 6) 单日最大亏损 -> 熔断
	if ctx.DayPnL/total <= e.MaxDayLossRatio {
		return e.reject(sig, "R_LOSS", "pnl", "HALT", "触及单日最大亏损，策略熔断")
	}

	// 7) 换手率上限（Redis 计数器）
	turn := e.cache.Incr("quant:trade:freq:" + sig.StrategyID)
	if turn > e.MaxTurnover {
		return e.reject(sig, "R_TURN", "turnover", "REJECT", "当日换手超限")
	}

	return true, ""
}

func (e *PreTradeEngine) reject(sig common.Signal, rule, dim, action, reason string) (bool, string) {
	e.ledger.SaveRiskLog(sig.StrategyID, rule, dim, action, reason)
	return false, reason
}
