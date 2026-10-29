// Command main 编排一个完整交易日：盘前初始化 -> 盘中闭环 -> 盘后对账归因。
// 五层装配：数据中台 -> 策略引擎 -> 风控引擎 -> 交易执行 -> 资产台账。
package main

import (
	"fmt"
	"math"
	"strconv"

	"quant/internal/common"
	"quant/internal/data"
	"quant/internal/execution"
	"quant/internal/ledger"
	"quant/internal/risk"
	"quant/internal/strategy"
)

func main() {
	accountID := "ACC01"
	symbols := []string{"600000", "600036", "000001", "600519", "300750", "000858", "601012"}

	// ---------- 基础设施（生产替换为 Redis / Kafka / FIX / MySQL）----------
	cache := common.NewMemoryCache()
	bus := common.NewMemoryBus()
	book := ledger.NewPortfolio(accountID, 100_000_000)
	broker := execution.NewMockBroker()
	oms := execution.NewOMS(cache, bus, broker, book)

	// ---------- 盘前 9:00-9:30：初始化股票池/黑名单/涨跌停/风控参数 ----------
	fmt.Println("================ 盘前初始化 ================")
	fc := data.NewFactorCache(cache)
	intIDs := map[string]int{}
	for i, s := range symbols {
		if id, err := strconv.Atoi(s); err == nil {
			intIDs[s] = id
		} else {
			intIDs[s] = 900000 + i
		}
	}
	fc.InitPool(symbols, []string{"ST002"}, intIDs)
	fmt.Printf("股票池=%d 黑名单=%d 交易日状态=%s\n", len(symbols), 1, mustGet(cache, data.KeyStatus))

	// 风控引擎
	preRisk := risk.NewPreTradeEngine(cache, book, intIDs)
	intraRisk := risk.NewIntradayMonitor(cache, book, bus)
	postRisk := risk.NewPostTradeAnalyzer()

	// 事前风控拦截演示（确定性用例）
	demoRiskChecks(cache, preRisk, book, intIDs)

	// 策略引擎（因子权重）
	engine := strategy.NewMultiFactorEngine(map[string]float64{
		"momentum_20d": 1.0,
		"rev_5d":       0.3,
		"vol_20d":      -0.5, // 低波动优先
	})
	gen := strategy.NewGenerator(engine, 3, 0.07, 2000) // Top3，单票 7%，防抖 2s

	// ---------- 盘中 9:30-15:00：事件驱动主循环 ----------
	fmt.Println("\n================ 盘中交易 ================")
	feed := data.NewMockFeed(symbols, 60)
	history := map[string][]float64{}
	prices := map[string]float64{}
	industry := map[string]string{
		"600000": "801780", "600036": "801780", "000001": "801780",
		"600519": "801120", "000858": "801120", "300750": "801730", "601012": "801730",
	}
	var signalCount, passCount, rejectCount int
	ordSeq := 0

	for tickIdx := 0; ; tickIdx++ {
		t, ok := feed.Next()
		if !ok {
			break
		}
		// 1) 行情入缓存 + 因子计算（生产中由 Flink 完成）
		fc.WriteTick(t)
		history[t.Symbol] = append(history[t.Symbol], t.Last)
		prices[t.Symbol] = t.Last
		for name, v := range computeFactors(history[t.Symbol]) {
			fc.WriteFactor(common.Factor{Symbol: t.Symbol, Name: name, Value: v, Ts: t.Ts})
		}
		// 2) 事中异常行情监控
		intraRisk.OnTick(t)

		// 每 3 个 tick 触发一次选股（降低换手，模拟中频调仓）
		if tickIdx%3 != 0 || tickIdx < len(symbols)*3 {
			continue
		}
		if intraRisk.Halted() {
			fmt.Println("   [熔断] 异常行情，暂停新信号")
			continue
		}

		// 3) 策略生成信号
		pos := book.Positions()
		sigs := gen.Generate(fc, symbols, book.Capital().Total, pos, prices, t.Ts)
		sigs = strategy.Aggregate(sigs)

		for _, sig := range sigs {
			signalCount++
			// 4) 事前风控拦截
			ctx := &risk.Context{
				Positions: pos,
				Capital:   ptrCapital(book.Capital()),
				Industry:  industry,
				Prices:    prices,
				DayPnL:    book.RealizedPnL(),
			}
			if ok, reason := preRisk.Check(sig, ctx); !ok {
				rejectCount++
				fmt.Printf("   [拦截] %s %s qty=%d -> %s\n", sig.Side, sig.Symbol, sig.Qty, reason)
				continue
			}
			passCount++

			// 5) 智能拆单
			parent := common.Order{
				OrderID:   fmt.Sprintf("ORD-%06d", nextID(&ordSeq)),
				AccountID: accountID, StrategyID: sig.StrategyID,
				Symbol: sig.Symbol, Side: sig.Side, OrderType: 1,
				OrigQty: sig.Qty, Price: sig.Price,
				Status: common.Init, CreatedAt: t.Ts, UpdatedAt: t.Ts,
			}
			algo := execution.TWAP
			if sig.Qty > 20000 {
				algo = execution.VWAP
			}
			children := execution.Slice(parent, algo, 3, 1000, t.Ts)
			for _, child := range children {
				oms.Submit(&child)
			}
		}
	}

	// ---------- 盘后 15:00 后：对账 + 归因 ----------
	fmt.Println("\n================ 盘后清算 ================")
	fmt.Printf("信号总数=%d 放行=%d 拦截=%d\n", signalCount, passCount, rejectCount)
	fmt.Println(book.Snapshot())

	// 三账合一（此处以台账真值 vs 缓存实时值对账）
	expected := map[string]int64{}
	for s, p := range book.Positions() {
		expected[s] = p.Qty
	}
	actual := map[string]int64{}
	for _, s := range symbols {
		h := cache.HGetAll("quant:position:real:" + s)
		if q, err := strconv.ParseInt(h["qty"], 10, 64); err == nil && q > 0 {
			actual[s] = q
		}
	}
	diffs, ok := ledger.Reconcile(expected, actual)
	fmt.Println(ledger.Report(diffs, ok))

	// 绩效归因（演示用收益序列）
	perf := postRisk.Analyze([]float64{0.01, -0.005, 0.002, 0.004, -0.001, 0.003})
	fmt.Printf("绩效: 夏普=%.2f 最大回撤=%.2f%% 胜率=%.0f%% 盈亏比=%.2f\n",
		perf.Sharpe, perf.MaxDrawdown*100, perf.WinRate*100, perf.ProfitFactor)
	if postRisk.ShouldHalt(perf) {
		fmt.Println("   [事后风控] 触发关停条件，次日 halt")
	}
	fmt.Println("\n全链路闭环完成：行情 -> 因子 -> 信号 -> 风控 -> 拆单 -> 成交 -> 台账 -> 对账")
}

// demoRiskChecks 事前风控拦截演示：验证涨停/黑名单/超仓位/超手数四类规则。
func demoRiskChecks(cache common.Cache, preRisk *risk.PreTradeEngine, book *ledger.Portfolio, intIDs map[string]int) {
	fmt.Println("\n---- 事前风控拦截演示 ----")
	// 标记 600000 涨停
	if id, ok := intIDs["600000"]; ok {
		cache.SetBit(data.KeyLimitUp, id, 1)
	}
	capital := book.Capital()
	mkCtx := func() *risk.Context {
		return &risk.Context{
			Positions: book.Positions(),
			Capital:   &capital,
			Industry:  map[string]string{"600519": "801120", "601012": "801730"},
			Prices:    map[string]float64{"600000": 10, "600519": 1700, "601012": 60},
			DayPnL:    0,
		}
	}
	cases := []struct {
		name string
		sig  common.Signal
	}{
		{"涨停禁买", common.Signal{StrategyID: "demo", Symbol: "600000", Side: common.Buy, Qty: 1000, Price: 10}},
		{"黑名单/ST", common.Signal{StrategyID: "demo", Symbol: "ST002", Side: common.Buy, Qty: 1000, Price: 20}},
		{"超单票仓位", common.Signal{StrategyID: "demo", Symbol: "600519", Side: common.Buy, Qty: 100000, Price: 1700}},
		{"超单笔手数", common.Signal{StrategyID: "demo", Symbol: "601012", Side: common.Buy, Qty: 600000, Price: 60}},
	}
	for _, c := range cases {
		ok, reason := preRisk.Check(c.sig, mkCtx())
		status := "放行"
		if !ok {
			status = "拦截"
		}
		fmt.Printf("   [%s] %-8s -> %s\n", status, c.name, reason)
	}
	// 复原涨停标记，避免影响后续实盘循环
	if id, ok := intIDs["600000"]; ok {
		cache.SetBit(data.KeyLimitUp, id, 0)
	}
}

// computeFactors 计算演示因子（生产由因子工厂批量计算）
func computeFactors(h []float64) map[string]float64 {
	if len(h) < 2 {
		return map[string]float64{"momentum_20d": 0, "rev_5d": 0, "vol_20d": 0}
	}
	last := h[len(h)-1]
	first := h[0]
	mom := 0.0
	if first != 0 {
		mom = last/first - 1
	}
	rev := 0.0
	if len(h) >= 5 {
		base := h[len(h)-5]
		if last != 0 {
			rev = base/last - 1
		}
	}
	// 日波动率（用 tick 收益标准差近似）
	var mean, sum float64
	for i := 1; i < len(h); i++ {
		r := h[i]/h[i-1] - 1
		mean += r
	}
	mean /= float64(len(h) - 1)
	for i := 1; i < len(h); i++ {
		r := h[i]/h[i-1] - 1
		sum += (r - mean) * (r - mean)
	}
	vol := math.Sqrt(sum / float64(len(h)-1))
	return map[string]float64{"momentum_20d": mom, "rev_5d": rev, "vol_20d": vol}
}

func mustGet(c common.Cache, k string) string {
	v, _ := c.Get(k)
	return v
}

func ptrCapital(c common.Capital) *common.Capital { return &c }

func nextID(n *int) int {
	*n = *n + 1
	return *n
}
