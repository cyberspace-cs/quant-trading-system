// Package data 数据中台：行情接入 + 清洗对齐 + 因子写入 Redis。
// 生产：C++/Go 行情客户端 -> Kafka(market.tick) -> Flink 清洗对齐 -> Redis/ClickHouse。
// 演示：MockFeed 生成行情序列，等价于回放历史 Tick。
package data

import (
	"math/rand"

	"quant/internal/common"
)

// Feed 行情源
type Feed interface {
	Next() (common.Tick, bool)
}

// MockFeed 模拟行情源：对给定标的生成带随机游走的 Tick 序列。
type MockFeed struct {
	symbols []string
	prices  map[string]float64
	ticks   []common.Tick
	idx     int
}

// NewMockFeed 生成 n 个 Tick（默认 5 档盘口）
func NewMockFeed(symbols []string, n int) *MockFeed {
	f := &MockFeed{symbols: symbols, prices: map[string]float64{}}
	for _, s := range symbols {
		f.prices[s] = 10 + rand.Float64()*90
	}
	ts := common.NowMs()
	for i := 0; i < n; i++ {
		for _, s := range symbols {
			p := f.prices[s]
			p = p * (1 + (rand.Float64()-0.5)*0.002) // ±0.1% 随机游走
			f.prices[s] = p
			var ask, bid [5]float64
			for k := 0; k < 5; k++ {
				ask[k] = round2(p + float64(k+1)*0.01)
				bid[k] = round2(p - float64(k+1)*0.01)
			}
			f.ticks = append(f.ticks, common.Tick{
				Symbol: s,
				Last:   round2(p),
				Ask:    ask,
				Bid:    bid,
				Vol:    int64(1000 + rand.Intn(5000)),
				Ts:     ts + int64(i*1000),
			})
		}
	}
	return f
}

func (f *MockFeed) Next() (common.Tick, bool) {
	if f.idx >= len(f.ticks) {
		return common.Tick{}, false
	}
	t := f.ticks[f.idx]
	f.idx++
	return t, true
}

func round2(x float64) float64 {
	return float64(int64(x*100+0.5)) / 100
}
