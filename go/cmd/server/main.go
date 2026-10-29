// Command server 启动量化工作台：五层引擎 + 仿真循环 + HTTP/SSE API + 前端静态服务。
package main

import (
	"flag"
	"log"
	"math"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"quant/internal/api"
	"quant/internal/common"
	"quant/internal/data"
	"quant/internal/execution"
	"quant/internal/ledger"
	"quant/internal/risk"
	"quant/internal/strategy"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP 监听地址")
	webDir := flag.String("web", "../web", "前端静态目录")
	secret := flag.String("secret", "quant-dev-secret-change-in-production", "JWT 签名密钥（生产从 KMS 注入）")
	flag.Parse()

	accountID := "ACC01"
	initial := 100_000_000.0
	symbols := []string{"600000", "600036", "000001", "600519", "300750", "000858", "601012"}
	industry := map[string]string{
		"600000": "银行", "600036": "银行", "000001": "银行",
		"600519": "白酒", "000858": "白酒", "300750": "新能源", "601012": "新能源",
	}

	cache := common.NewMemoryCache()
	bus := common.NewMemoryBus()
	book := ledger.NewPortfolio(accountID, initial)
	broker := execution.NewMockBroker()
	oms := execution.NewOMS(cache, bus, broker, book)

	intIDs := map[string]int{}
	for i, s := range symbols {
		if id, err := strconv.Atoi(s); err == nil {
			intIDs[s] = id
		} else {
			intIDs[s] = 900000 + i
		}
	}

	fc := data.NewFactorCache(cache)
	fc.InitPool(symbols, []string{"ST002"}, intIDs)

	preRisk := risk.NewPreTradeEngine(cache, book, intIDs)
	intraRisk := risk.NewIntradayMonitor(cache, book, bus)

	engine := strategy.NewMultiFactorEngine(map[string]float64{
		"momentum_20d": 1.0, "rev_5d": 0.3, "vol_20d": -0.5,
	})
	gen := strategy.NewGenerator(engine, 3, 0.07, 1500)

	state := api.NewAppState(accountID, initial, book, cache, bus, oms, preRisk, symbols, industry, intIDs)
	state.Strategies = []*api.StrategyInfo{
		{ID: "alpha_mf", Name: "中频多因子选股", Type: "multi_factor", Weight: 1.0, Status: "on", Sharpe: 3.08},
		{ID: "intraday_mr", Name: "日内均值回归", Type: "intraday", Weight: 0.5, Status: "gray", Sharpe: 1.42},
		{ID: "ai_lstm", Name: "AI-LSTM 预测", Type: "ai", Weight: 0.8, Status: "off", Sharpe: 2.11},
	}
	state.RecordEquity()

	// 熔断开关：API 层 /system/halt 触发后，仿真循环立即停止生成新信号
	var halted atomic.Bool

	// ---------- 仿真交易循环（后台）----------
	go func() {
		feed := data.NewMockFeed(symbols, 500000)
		history := map[string][]float64{}
		prices := map[string]float64{}
		round := 0
		ordSeq := 0
		for {
			for range symbols {
				t, ok := feed.Next()
				if !ok {
					return
				}
				fc.WriteTick(t)
				history[t.Symbol] = append(history[t.Symbol], t.Last)
				prices[t.Symbol] = t.Last
				fs := computeFactors(history[t.Symbol])
				for name, v := range fs {
					fc.WriteFactor(common.Factor{Symbol: t.Symbol, Name: name, Value: v, Ts: t.Ts})
				}
				state.RecordTick(t, fs)
				intraRisk.OnTick(t)
			}
			round++

			if round%25 == 0 && !intraRisk.Halted() && !halted.Load() && state.TradeStatus != "halt" {
				pos := book.Positions()
				sigs := gen.Generate(fc, symbols, state.TotalAssets(), pos, prices, common.NowMs())
				for _, sig := range strategy.Aggregate(sigs) {
					state.RecordSignal(sig)
					ctx := &risk.Context{
						Positions: pos, Capital: ptrCapital(book.Capital()),
						Industry: industry, Prices: prices, DayPnL: book.RealizedPnL(),
					}
					if ok, _ := preRisk.Check(sig, ctx); !ok {
						continue
					}
					ordSeq++
					parent := common.Order{
						OrderID:   "ORD-" + time.Now().Format("150405") + "-" + strconv.Itoa(ordSeq),
						AccountID: accountID, StrategyID: sig.StrategyID, Symbol: sig.Symbol,
						Side: sig.Side, OrderType: 1, OrigQty: sig.Qty, Price: sig.Price,
						Status: common.Init, CreatedAt: common.NowMs(), UpdatedAt: common.NowMs(),
					}
					algo := execution.TWAP
					if sig.Qty > 20000 {
						algo = execution.VWAP
					}
					var filled int64
					for _, child := range execution.Slice(parent, algo, 3, 1000, common.NowMs()) {
						oms.Submit(&child)
						filled += child.FilledQty
					}
					// 母单同样落台账（订单列表只展示母单，子单在详情里看拆单结果）
					parent.FilledQty = filled
					if filled >= parent.OrigQty {
						parent.Status = common.Filled
					} else if filled > 0 {
						parent.Status = common.Partial
					}
					book.SaveOrder(&parent)
				}
			}
			state.RecordEquity()
			time.Sleep(260 * time.Millisecond)
		}
	}()

	// ---------- 接口层：装配回调，让「改参数 / 一键熔断」真正作用到引擎 ----------
	app := api.NewServer(state, *webDir)
	app.SetSecret(*secret)
	app.OnHalt = func(h bool, reason string) {
		halted.Store(h)
		log.Printf("[halt] halt=%v reason=%s", h, reason)
	}
	app.OnParams = func(id string, p map[string]float64) {
		if id != "alpha_mf" {
			return
		}
		w := map[string]float64{"momentum_20d": 1.0, "rev_5d": 0.3, "vol_20d": -0.5}
		for k := range w {
			if v, ok := p[k]; ok {
				w[k] = v
			}
		}
		engine.SetWeights(w)
		log.Printf("[params] strategy=%s weights=%v", id, w)
	}

	srv := &http.Server{Addr: *addr, Handler: app.Handler()}
	log.Printf("quant workbench listening on %s  (web=%s)", *addr, *webDir)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func computeFactors(h []float64) map[string]float64 {
	if len(h) < 2 {
		return map[string]float64{"momentum_20d": 0, "rev_5d": 0, "vol_20d": 0}
	}
	last, first := h[len(h)-1], h[0]
	mom := 0.0
	if first != 0 {
		mom = last/first - 1
	}
	rev := 0.0
	if len(h) >= 5 && last != 0 {
		rev = h[len(h)-5]/last - 1
	}
	var mean float64
	for i := 1; i < len(h); i++ {
		mean += h[i]/h[i-1] - 1
	}
	mean /= float64(len(h) - 1)
	var sum float64
	for i := 1; i < len(h); i++ {
		r := h[i]/h[i-1] - 1
		sum += (r - mean) * (r - mean)
	}
	return map[string]float64{"momentum_20d": mom, "rev_5d": rev, "vol_20d": math.Sqrt(sum / float64(len(h)-1))}
}

func ptrCapital(c common.Capital) *common.Capital { return &c }
