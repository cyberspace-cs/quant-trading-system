package risk

import (
	"testing"

	"quant/internal/common"
	"quant/internal/data"
	"quant/internal/ledger"
)

// TestPreTradeRejects 事前风控：涨停 / 黑名单 / 超手数 必须拦截，合法单放行
func TestPreTradeRejects(t *testing.T) {
	cache := common.NewMemoryCache()
	book := ledger.NewPortfolio("ACC01", 100_000_000)
	intIDs := map[string]int{"600000": 600000, "600519": 600519, "ST002": 900002}

	cache.SetBit(data.KeyLimitUp, 600000, 1)
	cache.SAdd(data.KeyBlacklist, "ST002")

	e := NewPreTradeEngine(cache, book, intIDs)
	cap := book.Capital()
	ctx := &Context{
		Positions: map[string]*common.Position{},
		Capital:   &cap,
		Industry:  map[string]string{"600519": "801120"},
		Prices:    map[string]float64{"600519": 1700},
		DayPnL:    0,
	}

	cases := []struct {
		name string
		sig  common.Signal
		want bool // true=放行
	}{
		{"涨停禁买", common.Signal{Symbol: "600000", Side: common.Buy, Qty: 1000, Price: 10, StrategyID: "t1"}, false},
		{"黑名单", common.Signal{Symbol: "ST002", Side: common.Buy, Qty: 1000, Price: 10, StrategyID: "t2"}, false},
		{"超手数", common.Signal{Symbol: "600519", Side: common.Buy, Qty: 600000, Price: 1700, StrategyID: "t3"}, false},
		{"合法小单", common.Signal{Symbol: "600519", Side: common.Buy, Qty: 1000, Price: 1700, StrategyID: "t4"}, true},
	}
	for _, c := range cases {
		ok, _ := e.Check(c.sig, ctx)
		if ok != c.want {
			t.Fatalf("%s: got allow=%v want %v", c.name, ok, c.want)
		}
	}
}
