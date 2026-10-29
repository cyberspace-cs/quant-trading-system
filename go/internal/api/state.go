// Package api 对外 HTTP / SSE 接口层：把五层引擎的状态暴露给前端工作台。
// 这是当前仓库最大的缺口——此前只有 CLI，没有任何可供前端消费的接口。
package api

import (
	"math"
	"sort"
	"sync"
	"time"

	"quant/internal/common"
	"quant/internal/data"
	"quant/internal/execution"
	"quant/internal/ledger"
	"quant/internal/risk"
)

// StrategyInfo 策略运行态
type StrategyInfo struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Type   string  `json:"type"`
	Weight float64 `json:"weight"`
	Status string  `json:"status"` // on/off/gray/halt
	PnL    float64 `json:"pnl"`
	Sharpe float64 `json:"sharpe"`
}

// EquityPoint 净值点
type EquityPoint struct {
	T string  `json:"t"`
	V float64 `json:"v"`
}

// AppState 全局运行态（供 API 读取，仿真循环写入）
type AppState struct {
	mu sync.RWMutex

	AccountID string
	Initial   float64

	Book    *ledger.Portfolio
	Cache   common.Cache
	Bus     common.EventBus
	OMS     *execution.OMS
	PreRisk *risk.PreTradeEngine

	Symbols  []string
	Industry map[string]string
	IntIDs   map[string]int

	Market  map[string]common.Tick
	Factors map[string]map[string]float64

	Signals    []common.Signal
	Strategies []*StrategyInfo
	Equity     []EquityPoint
	PeakNav    float64

	LatencyUs   int
	TradeDate   string
	TradeStatus string // pre/open/mid/close
	RiskRejects int
}

func NewAppState(accountID string, initial float64, book *ledger.Portfolio,
	cache common.Cache, bus common.EventBus, oms *execution.OMS, pre *risk.PreTradeEngine,
	symbols []string, industry map[string]string, intIDs map[string]int) *AppState {
	return &AppState{
		AccountID: accountID, Initial: initial, Book: book, Cache: cache, Bus: bus,
		OMS: oms, PreRisk: pre, Symbols: symbols, Industry: industry, IntIDs: intIDs,
		Market: map[string]common.Tick{}, Factors: map[string]map[string]float64{},
		PeakNav: 1.0, LatencyUs: 320, TradeDate: time.Now().Format("2006-01-02"),
		TradeStatus: "open",
	}
}

// ---------- 写入（仿真循环调用）----------

func (s *AppState) RecordTick(t common.Tick, factors map[string]float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Market[t.Symbol] = t
	if s.Factors[t.Symbol] == nil {
		s.Factors[t.Symbol] = map[string]float64{}
	}
	for k, v := range factors {
		s.Factors[t.Symbol][k] = v
	}
}

func (s *AppState) RecordSignal(sig common.Signal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Signals = append(s.Signals, sig)
	if len(s.Signals) > 500 {
		s.Signals = s.Signals[len(s.Signals)-500:]
	}
}

// SignalView 信号
type SignalView struct {
	Strategy string  `json:"strategy"`
	Symbol   string  `json:"symbol"`
	Side     string  `json:"side"`
	Qty      int64   `json:"qty"`
	Price    float64 `json:"price"`
	Time     string  `json:"time"`
}

func (s *AppState) SignalList(limit int) []SignalView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := s.Signals
	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}
	out := make([]SignalView, 0, len(all))
	for _, sg := range all {
		out = append(out, SignalView{
			Strategy: sg.StrategyID, Symbol: sg.Symbol, Side: sg.Side.String(),
			Qty: sg.Qty, Price: round2(sg.Price),
			Time: time.UnixMilli(sg.Ts).Format("15:04:05"),
		})
	}
	return out
}

func (s *AppState) RecordEquity() {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := s.mtmTotal()
	nav := 0.0
	if s.Initial > 0 {
		nav = total / s.Initial
	}
	if nav > s.PeakNav {
		s.PeakNav = nav
	}
	s.Equity = append(s.Equity, EquityPoint{T: time.Now().Format("15:04:05"), V: round4(nav)})
	if len(s.Equity) > 300 {
		s.Equity = s.Equity[len(s.Equity)-300:]
	}
	s.LatencyUs = 180 + int(time.Now().UnixNano()%420)
	s.RiskRejects = s.Book.RiskLogCount()
}

func (s *AppState) SetStatus(st string) {
	s.mu.Lock()
	s.TradeStatus = st
	s.mu.Unlock()
}

// mtmTotal 按最新行情盯市计算总资产（调用方需持有 s.mu）
func (s *AppState) mtmTotal() float64 {
	c := s.Book.Capital()
	total := c.Available + c.Frozen
	for sym, p := range s.Book.Positions() {
		if p.Qty == 0 {
			continue
		}
		px := s.Market[sym].Last
		if px == 0 {
			px = p.AvgCost
		}
		total += float64(p.Qty) * px
	}
	return total
}

// ---------- 读取（API 调用）----------

// Overview 总览
type Overview struct {
	AccountID   string        `json:"account_id"`
	TradeDate   string        `json:"trade_date"`
	Status      string        `json:"status"`
	Total       float64       `json:"total"`
	Available   float64       `json:"available"`
	Frozen      float64       `json:"frozen"`
	Nav         float64       `json:"nav"`
	DayPnL      float64       `json:"day_pnl"`
	DayPnLPct   float64       `json:"day_pnl_pct"`
	MaxDrawdown float64       `json:"max_drawdown"`
	Sharpe      float64       `json:"sharpe"`
	Turnover    float64       `json:"turnover"`
	LatencyUs   int           `json:"latency_us"`
	Positions   int           `json:"positions"`
	Orders      int           `json:"orders"`
	Fills       int           `json:"fills"`
	RiskRejects int           `json:"risk_rejects"`
	Equity      []EquityPoint `json:"equity"`
}

func (s *AppState) Overview() Overview {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cap := s.Book.Capital()
	total := s.mtmTotal()
	nav := 0.0
	if s.Initial > 0 {
		nav = total / s.Initial
	}
	dd := 0.0
	if s.PeakNav > 0 {
		dd = (nav - s.PeakNav) / s.PeakNav
	}
	// 换手：成交额 / 总资产 / 2
	fillAmt := 0.0
	for _, f := range s.Book.Fills() {
		fillAmt += f.FillAmount
	}
	turnover := 0.0
	if total > 0 {
		turnover = fillAmt / total / 2
	}
	return Overview{
		AccountID: s.AccountID, TradeDate: s.TradeDate, Status: s.TradeStatus,
		Total: round2(total), Available: round2(cap.Available), Frozen: round2(cap.Frozen),
		Nav: round4(nav), DayPnL: round2(total - s.Initial),
		DayPnLPct:   round4((total - s.Initial) / s.Initial),
		MaxDrawdown: round4(dd), Sharpe: round2(sharpeOf(s.Equity)),
		Turnover: round4(turnover), LatencyUs: s.LatencyUs,
		Positions: len(s.Book.Positions()), Orders: s.Book.OrderCount(), Fills: s.Book.FillCount(),
		RiskRejects: s.Book.RiskLogCount(), Equity: s.Equity,
	}
}

// PositionView 持仓
type PositionView struct {
	Symbol      string  `json:"symbol"`
	Qty         int64   `json:"qty"`
	AvgCost     float64 `json:"avg_cost"`
	MarketValue float64 `json:"market_value"`
	PnL         float64 `json:"pnl"`
	PnLPct      float64 `json:"pnl_pct"`
	Industry    string  `json:"industry"`
}

func (s *AppState) Positions() []PositionView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pos := s.Book.Positions()
	out := make([]PositionView, 0, len(pos))
	for _, p := range pos {
		if p.Qty == 0 {
			continue
		}
		px := s.Market[p.Symbol].Last
		if px == 0 {
			px = p.AvgCost
		}
		mv := float64(p.Qty) * px
		pnl := (px - p.AvgCost) * float64(p.Qty)
		pnlPct := 0.0
		if p.AvgCost > 0 {
			pnlPct = px/p.AvgCost - 1
		}
		out = append(out, PositionView{
			Symbol: p.Symbol, Qty: p.Qty, AvgCost: round4(p.AvgCost),
			MarketValue: round2(mv), PnL: round2(pnl), PnLPct: round4(pnlPct),
			Industry: s.Industry[p.Symbol],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MarketValue > out[j].MarketValue })
	return out
}

// OrderView 订单
type OrderView struct {
	OrderID  string  `json:"order_id"`
	Symbol   string  `json:"symbol"`
	Side     string  `json:"side"`
	Strategy string  `json:"strategy"`
	Qty      int64   `json:"qty"`
	Filled   int64   `json:"filled"`
	Price    float64 `json:"price"`
	Status   string  `json:"status"`
	Time     string  `json:"time"`
}

func (s *AppState) Orders(limit int) []OrderView {
	all := s.Book.Orders()
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	out := make([]OrderView, 0, len(all))
	for _, o := range all {
		out = append(out, OrderView{
			OrderID: o.OrderID, Symbol: o.Symbol, Side: o.Side.String(),
			Strategy: o.StrategyID, Qty: o.OrigQty, Filled: o.FilledQty,
			Price: round2(o.Price), Status: common.StatusName(o.Status),
			Time: time.UnixMilli(o.CreatedAt).Format("15:04:05"),
		})
	}
	return out
}

// FillView 成交
type FillView struct {
	FillID string  `json:"fill_id"`
	Symbol string  `json:"symbol"`
	Side   string  `json:"side"`
	Qty    int64   `json:"qty"`
	Price  float64 `json:"price"`
	Amount float64 `json:"amount"`
	Time   string  `json:"time"`
}

func (s *AppState) Fills(limit int) []FillView {
	all := s.Book.Fills()
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	out := make([]FillView, 0, len(all))
	for _, f := range all {
		out = append(out, FillView{
			FillID: f.FillID, Symbol: f.Symbol, Side: f.Side.String(),
			Qty: f.FilledQty, Price: round2(f.FillPrice), Amount: round2(f.FillAmount),
			Time: time.UnixMilli(f.FillTime).Format("15:04:05"),
		})
	}
	return out
}

// RiskLogView 风控日志
type RiskLogView struct {
	RuleID    string `json:"rule_id"`
	Dimension string `json:"dimension"`
	Action    string `json:"action"`
	Reason    string `json:"reason"`
	Time      string `json:"time"`
}

func (s *AppState) RiskLogs(limit int) []RiskLogView {
	all := s.Book.RiskLogs()
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	out := make([]RiskLogView, 0, len(all))
	for _, l := range all {
		out = append(out, RiskLogView{
			RuleID: l.RuleID, Dimension: l.Dimension, Action: l.Action,
			Reason: l.Reason, Time: time.UnixMilli(l.At).Format("15:04:05"),
		})
	}
	return out
}

func (s *AppState) StrategyList() []*StrategyInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*StrategyInfo, len(s.Strategies))
	for i, st := range s.Strategies {
		cp := *st
		out[i] = &cp
	}
	return out
}

func (s *AppState) SwitchStrategy(id, status string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.Strategies {
		if st.ID == id {
			st.Status = status
			return true
		}
	}
	return false
}

// Performance 绩效
type Performance struct {
	Sharpe       float64       `json:"sharpe"`
	MaxDrawdown  float64       `json:"max_drawdown"`
	WinRate      float64       `json:"win_rate"`
	ProfitFactor float64       `json:"profit_factor"`
	Calmar       float64       `json:"calmar"`
	AnnualReturn float64       `json:"annual_return"`
	Curve        []EquityPoint `json:"curve"`
}

func (s *AppState) Performance() Performance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	curve := s.Equity
	rets := make([]float64, 0, len(curve))
	for i := 1; i < len(curve); i++ {
		if curve[i-1].V > 0 {
			rets = append(rets, curve[i].V/curve[i-1].V-1)
		}
	}
	win, loss, winSum, lossSum := 0, 0, 0.0, 0.0
	for _, r := range rets {
		if r > 0 {
			win++
			winSum += r
		} else if r < 0 {
			loss++
			lossSum += -r
		}
	}
	winRate, pf := 0.0, 0.0
	if win+loss > 0 {
		winRate = float64(win) / float64(win+loss)
	}
	if lossSum > 0 {
		pf = winSum / lossSum
	}
	ann := 0.0
	if len(curve) > 0 && curve[0].V > 0 {
		ann = math.Pow(curve[len(curve)-1].V/curve[0].V, 252.0/float64(max(len(rets), 1))) - 1
	}
	md := 0.0
	if s.PeakNav > 0 && len(curve) > 0 {
		md = (curve[len(curve)-1].V - s.PeakNav) / s.PeakNav
	}
	calmar := 0.0
	if md < 0 {
		calmar = ann / math.Abs(md)
	}
	return Performance{
		Sharpe: round2(sharpeOf(curve)), MaxDrawdown: round4(md), WinRate: round4(winRate),
		ProfitFactor: round2(pf), Calmar: round2(calmar), AnnualReturn: round4(ann), Curve: curve,
	}
}

// FactorView 因子
type FactorView struct {
	Symbol  string             `json:"symbol"`
	Factors map[string]float64 `json:"factors"`
}

func (s *AppState) FactorOf(symbol string) FactorView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return FactorView{Symbol: symbol, Factors: s.Factors[symbol]}
}

// MarketView 行情
type MarketView struct {
	Symbol string  `json:"symbol"`
	Last   float64 `json:"last"`
	Vol    int64   `json:"vol"`
	Ask1   float64 `json:"ask1"`
	Bid1   float64 `json:"bid1"`
}

func (s *AppState) MarketList() []MarketView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]MarketView, 0, len(s.Market))
	for _, sym := range s.Symbols {
		if t, ok := s.Market[sym]; ok {
			out = append(out, MarketView{Symbol: sym, Last: t.Last, Vol: t.Vol, Ask1: t.Ask[0], Bid1: t.Bid[0]})
		}
	}
	return out
}

// PoolView 股票池
type PoolView struct {
	Pool      []string `json:"pool"`
	Blacklist []string `json:"blacklist"`
	LimitUp   []string `json:"limit_up"`
	LimitDown []string `json:"limit_down"`
}

func (s *AppState) Pool() PoolView {
	up, down := []string{}, []string{}
	for _, sym := range s.Symbols {
		if id, ok := s.IntIDs[sym]; ok {
			if s.Cache.GetBit(data.KeyLimitUp, id) == 1 {
				up = append(up, sym)
			}
			if s.Cache.GetBit(data.KeyLimitDown, id) == 1 {
				down = append(down, sym)
			}
		}
	}
	return PoolView{Pool: s.Symbols, Blacklist: []string{"ST002"}, LimitUp: up, LimitDown: down}
}

// ReconcileView 对账
type ReconcileView struct {
	OK    bool          `json:"ok"`
	Diffs []ledger.Diff `json:"diffs"`
	Time  string        `json:"time"`
}

func (s *AppState) Reconcile() ReconcileView {
	expected := map[string]int64{}
	for sym, p := range s.Book.Positions() {
		expected[sym] = p.Qty
	}
	actual := map[string]int64{}
	for _, sym := range s.Symbols {
		h := s.Cache.HGetAll("quant:position:real:" + sym)
		var q int64
		if v, ok := h["qty"]; ok {
			for _, c := range v {
				q = q*10 + int64(c-'0')
			}
		}
		if q > 0 {
			actual[sym] = q
		}
	}
	diffs, ok := ledger.Reconcile(expected, actual)
	if diffs == nil {
		diffs = []ledger.Diff{}
	}
	return ReconcileView{OK: ok, Diffs: diffs, Time: time.Now().Format("15:04:05")}
}

func (s *AppState) CancelOrder(id string) bool { return s.Book.CancelOrder(id) }

// ---------- 工具 ----------

func sharpeOf(eq []EquityPoint) float64 {
	if len(eq) < 3 {
		return 0
	}
	rets := make([]float64, 0, len(eq)-1)
	for i := 1; i < len(eq); i++ {
		if eq[i-1].V > 0 {
			rets = append(rets, eq[i].V/eq[i-1].V-1)
		}
	}
	if len(rets) < 2 {
		return 0
	}
	var mean float64
	for _, r := range rets {
		mean += r
	}
	mean /= float64(len(rets))
	var v float64
	for _, r := range rets {
		v += (r - mean) * (r - mean)
	}
	std := math.Sqrt(v / float64(len(rets)))
	if std == 0 {
		return 0
	}
	return mean / std * math.Sqrt(252)
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }
func round4(x float64) float64 { return math.Round(x*10000) / 10000 }

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
