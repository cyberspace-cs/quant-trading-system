// Package strategy 策略引擎：多因子打分 -> 排序 -> 权重 -> 调仓信号。
// 研究侧 Python 训练模型、定义因子；实盘侧 Go 读 Redis 因子池批量打分，毫秒级生成信号。
package strategy

import (
	"sort"

	"quant/internal/data"
)

// MultiFactorEngine 多因子合成打分器。
// score = Σ w_i * zscore(factor_i)，因子已在中台完成行业/市值中性化。
type MultiFactorEngine struct {
	Weights map[string]float64 // factor_name -> weight
}

func NewMultiFactorEngine(weights map[string]float64) *MultiFactorEngine {
	return &MultiFactorEngine{Weights: weights}
}

// Score 计算单标的综合分；任一关键因子缺失返回 false（该标的本轮跳过）。
func (e *MultiFactorEngine) Score(fc *data.FactorCache, symbol string) (float64, bool) {
	score := 0.0
	for name, w := range e.Weights {
		v, ok := fc.ReadFactor(symbol, name)
		if !ok {
			return 0, false
		}
		score += w * v
	}
	return score, true
}

// Ranked 打分排序结果
type Ranked struct {
	Symbol string
	Score  float64
}

// Rank 对股票池打分并降序排序。
func (e *MultiFactorEngine) Rank(fc *data.FactorCache, symbols []string) []Ranked {
	out := make([]Ranked, 0, len(symbols))
	for _, s := range symbols {
		if sc, ok := e.Score(fc, s); ok {
			out = append(out, Ranked{Symbol: s, Score: sc})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}
