package risk

import (
	"math"
)

// Perf 绩效指标
type Perf struct {
	Sharpe       float64
	MaxDrawdown  float64
	WinRate      float64
	ProfitFactor float64
}

// PostTradeAnalyzer 事后风控 & 绩效归因：日终计算夏普 / 回撤 / 胜率 / 盈亏比。
type PostTradeAnalyzer struct{}

func NewPostTradeAnalyzer() *PostTradeAnalyzer { return &PostTradeAnalyzer{} }

// Analyze 根据日收益序列计算绩效（供 strategy_perf 落库）。
func (a *PostTradeAnalyzer) Analyze(dailyReturns []float64) Perf {
	n := len(dailyReturns)
	if n == 0 {
		return Perf{}
	}
	var mean float64
	for _, r := range dailyReturns {
		mean += r
	}
	mean /= float64(n)

	var varSum float64
	for _, r := range dailyReturns {
		varSum += (r - mean) * (r - mean)
	}
	std := math.Sqrt(varSum / float64(n))

	var sharpe float64
	if std > 0 {
		sharpe = mean / std * math.Sqrt(252) // 年化
	}

	// 最大回撤（按累计净值）
	nav := 1.0
	peak := 1.0
	maxDD := 0.0
	for _, r := range dailyReturns {
		nav *= 1 + r
		if nav > peak {
			peak = nav
		}
		dd := (nav - peak) / peak
		if dd < maxDD {
			maxDD = dd
		}
	}

	// 胜率 & 盈亏比
	wins, losses := 0, 0
	var winSum, lossSum float64
	for _, r := range dailyReturns {
		if r > 0 {
			wins++
			winSum += r
		} else if r < 0 {
			losses++
			lossSum += -r
		}
	}
	winRate := 0.0
	if wins+losses > 0 {
		winRate = float64(wins) / float64(wins+losses)
	}
	pf := 0.0
	if lossSum > 0 {
		pf = winSum / lossSum
	}

	return Perf{Sharpe: sharpe, MaxDrawdown: maxDD, WinRate: winRate, ProfitFactor: pf}
}

// ShouldHalt 事后关停判定：回撤超阈值 或 夏普转负。
func (a *PostTradeAnalyzer) ShouldHalt(p Perf) bool {
	return p.MaxDrawdown < -0.10 || p.Sharpe < 0
}
